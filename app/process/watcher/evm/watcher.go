/*
 * Copyright 2021 LimeChain Ltd.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package evm

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hashgraph/hedera-sdk-go/v2"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/evm/contracts/router"
	"github.com/limechain/hedera-eth-bridge-validator/app/core/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/client"
	qi "github.com/limechain/hedera-eth-bridge-validator/app/domain/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/repository"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/service"
	helper "github.com/limechain/hedera-eth-bridge-validator/app/helper/big-numbers"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/metrics"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/timestamp"
	"github.com/limechain/hedera-eth-bridge-validator/app/model/transfer"
	c "github.com/limechain/hedera-eth-bridge-validator/config"
	"github.com/limechain/hedera-eth-bridge-validator/constants"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type Watcher struct {
	repository repository.Status
	// A unique database identifier, used as a key to track the progress
	// of the given EVM watcher. Given that addresses between different
	// EVM networks might be the same, a concatenation between
	// <chain-id>-<contract-address> removes possible duplication.
	dbIdentifier      string
	contracts         service.Contracts
	prometheusService service.Prometheus
	evmClient         client.EVM
	logger            *log.Entry
	mappings          c.Assets
	targetBlock       uint64
	sleepDuration     time.Duration
	validator         bool
	filterConfig      FilterConfig
}

// Certain node providers (Alchemy, Infura) have a limitation on how many blocks
// eth_getLogs can process at once. For this to be mitigated, a maximum amount of blocks
// is introduced, splitting the request into chunks with a range of N.
// For example, a query for events with a range of 5 000 blocks, will be split into 10 queries, each having
// a range of 500 blocks
const defaultMaxLogsBlocks = int64(500)

// The default polling interval (in seconds) when querying for upcoming events/logs
const defaultSleepDuration = 15 * time.Second

type FilterConfig struct {
	abi               abi.ABI
	topics            [][]common.Hash
	addresses         []common.Address
	mintHash          common.Hash
	burnHash          common.Hash
	lockHash          common.Hash
	unlockHash        common.Hash
	memberUpdatedHash common.Hash
	maxLogsBlocks     int64
}

func NewWatcher(
	repository repository.Status,
	contracts service.Contracts,
	prometheusService service.Prometheus,
	evmClient client.EVM,
	mappings c.Assets,
	dbIdentifier string,
	startBlock int64,
	validator bool,
	pollingInterval time.Duration,
	maxLogsBlocks int64) *Watcher {
	currentBlock, err := evmClient.RetryBlockNumber()
	if err != nil {
		log.Fatalf("Could not retrieve latest block. Error: [%s].", err)
	}
	targetBlock := helper.Max(0, currentBlock-evmClient.BlockConfirmations())

	abi, err := abi.JSON(strings.NewReader(router.RouterABI))
	if err != nil {
		log.Fatalf("Failed to parse router ABI. Error: [%s]", err)
	}

	mintHash := abi.Events["Mint"].ID
	burnHash := abi.Events["Burn"].ID
	lockHash := abi.Events["Lock"].ID
	unlockHash := abi.Events["Unlock"].ID
	memberUpdatedHash := abi.Events["MemberUpdated"].ID

	topics := [][]common.Hash{
		{
			mintHash,
			burnHash,
			lockHash,
			unlockHash,
			memberUpdatedHash,
		},
	}

	addresses := []common.Address{
		contracts.Address(),
	}

	if maxLogsBlocks == 0 {
		maxLogsBlocks = defaultMaxLogsBlocks
	}

	filterConfig := FilterConfig{
		abi:               abi,
		topics:            topics,
		addresses:         addresses,
		mintHash:          mintHash,
		burnHash:          burnHash,
		lockHash:          lockHash,
		unlockHash:        unlockHash,
		memberUpdatedHash: memberUpdatedHash,
		maxLogsBlocks:     maxLogsBlocks,
	}

	if pollingInterval == 0 {
		pollingInterval = defaultSleepDuration
	} else {
		pollingInterval = pollingInterval * time.Second
	}

	if startBlock == 0 {
		_, err := repository.Get(dbIdentifier)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				err := repository.Create(dbIdentifier, int64(targetBlock))
				if err != nil {
					log.Fatalf("[%s] - Failed to create Transfer Watcher timestamp. Error: [%s]", dbIdentifier, err)
				}
				log.Tracef("[%s] - Created new Transfer Watcher timestamp [%s]", dbIdentifier, timestamp.ToHumanReadable(int64(targetBlock)))
			} else {
				log.Fatalf("[%s] - Failed to fetch last Transfer Watcher timestamp. Error: [%s]", dbIdentifier, err)
			}
		}
	} else {
		err := repository.Update(dbIdentifier, startBlock)
		if err != nil {
			log.Fatalf("[%s] - Failed to update Transfer Watcher Status timestamp. Error [%s]", dbIdentifier, err)
		}
		targetBlock = uint64(startBlock)
		log.Tracef("[%s] - Updated Transfer Watcher timestamp to [%s]", dbIdentifier, timestamp.ToHumanReadable(startBlock))
	}
	return &Watcher{
		repository:        repository,
		dbIdentifier:      dbIdentifier,
		contracts:         contracts,
		prometheusService: prometheusService,
		evmClient:         evmClient,
		logger:            c.GetLoggerFor(fmt.Sprintf("EVM Router Watcher [%s]", dbIdentifier)),
		mappings:          mappings,
		targetBlock:       targetBlock,
		validator:         validator,
		sleepDuration:     pollingInterval,
		filterConfig:      filterConfig,
	}
}

func (ew *Watcher) Watch(queue qi.Queue) {
	go ew.beginWatching(queue)

	ew.logger.Infof("Listening for events at contract [%s]", ew.dbIdentifier)
}

func (ew Watcher) beginWatching(queue qi.Queue) {
	fromBlock, err := ew.repository.Get(ew.dbIdentifier)
	if err != nil {
		ew.logger.Errorf("Failed to retrieve EVM Watcher Status fromBlock. Error: [%s]", err)
		time.Sleep(ew.sleepDuration)
		ew.beginWatching(queue)
		return
	}

	ew.logger.Infof("Processing events from [%d]", fromBlock)

	for {
		fromBlock, err := ew.repository.Get(ew.dbIdentifier)
		if err != nil {
			ew.logger.Errorf("Failed to retrieve EVM Watcher Status fromBlock. Error: [%s]", err)
			continue
		}

		currentBlock, err := ew.evmClient.RetryBlockNumber()
		if err != nil {
			ew.logger.Errorf("Failed to retrieve latest block number. Error [%s]", err)
			time.Sleep(ew.sleepDuration)
			continue
		}

		toBlock := int64(currentBlock - ew.evmClient.BlockConfirmations())
		if fromBlock > toBlock {
			time.Sleep(ew.sleepDuration)
			continue
		}

		if toBlock-fromBlock > ew.filterConfig.maxLogsBlocks {
			toBlock = fromBlock + ew.filterConfig.maxLogsBlocks
		}

		err = ew.processLogs(fromBlock, toBlock, queue)
		if err != nil {
			ew.logger.Errorf("Failed to process logs. Error: [%s].", err)
			time.Sleep(ew.sleepDuration)
			continue
		}

		time.Sleep(ew.sleepDuration)
	}
}

func (ew Watcher) processLogs(fromBlock, endBlock int64, queue qi.Queue) error {
	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetInt64(fromBlock),
		ToBlock:   new(big.Int).SetInt64(endBlock),
		Addresses: ew.filterConfig.addresses,
		Topics:    ew.filterConfig.topics,
	}

	logs, err := ew.evmClient.RetryFilterLogs(query)
	if err != nil {
		ew.logger.Errorf("Failed to filter logs. Error: [%s]", err)
		return err
	}

	for _, log := range logs {
		if len(log.Topics) > 0 {
			if log.Topics[0] == ew.filterConfig.lockHash {
				lock, err := ew.contracts.ParseLockLog(log)
				if err != nil {
					ew.logger.Errorf("Could not parse lock log [%s]. Error [%s].", lock.Raw.TxHash.String(), err)
					continue
				}
				ew.handleLockLog(lock, queue)
			} else if log.Topics[0] == ew.filterConfig.unlockHash {
				unlock, err := ew.contracts.ParseUnlockLog(log)
				if err != nil {
					ew.logger.Errorf("Could not parse unlock log [%s]. Error [%s].", unlock.Raw.TxHash.String(), err)
					continue
				}
				ew.handleUnlockLog(unlock)
			} else if log.Topics[0] == ew.filterConfig.mintHash {
				mint, err := ew.contracts.ParseMintLog(log)
				if err != nil {
					ew.logger.Errorf("Could not parse mint log [%s]. Error [%s].", mint.Raw.TxHash.String(), err)
					continue
				}
				ew.handleMintLog(mint)
			} else if log.Topics[0] == ew.filterConfig.burnHash {
				burn, err := ew.contracts.ParseBurnLog(log)
				if err != nil {
					ew.logger.Errorf("Could not parse burn log [%s]. Error [%s].", burn.Raw.TxHash.String(), err)
					continue
				}
				ew.handleBurnLog(burn, queue)
			} else if log.Topics[0] == ew.filterConfig.memberUpdatedHash {
				go ew.contracts.ReloadMembers()
			}
		}
	}

	// Given that the log filtering boundaries are inclusive,
	// the next time log filtering is done will start from the next block,
	// so that processing of duplicate events does not occur
	blockToBeUpdated := endBlock + 1

	err = ew.repository.Update(ew.dbIdentifier, blockToBeUpdated)
	if err != nil {
		ew.logger.Errorf("Failed to update latest processed block [%d]. Error: [%s]", blockToBeUpdated, err)
		return err
	}

	return nil
}

func (ew *Watcher) handleMintLog(eventLog *router.RouterMint) {
	ew.logger.Infof("[%s] - New Mint Event Log received.", eventLog.Raw.TxHash)

	if eventLog.Raw.Removed {
		ew.logger.Debugf("[%s] - Uncle block transaction was removed.", eventLog.Raw.TxHash)
		return
	}

	var chain *big.Int
	chain, e := ew.evmClient.ChainID(context.Background())
	if e != nil {
		ew.logger.Errorf("[%s] - Failed to retrieve chain ID.", eventLog.Raw.TxHash)
		return
	}
	transactionId := string(eventLog.TransactionId)
	sourceChainId := eventLog.SourceChain.Int64()
	targetChainId := chain.Int64()
	oppositeToken := ew.mappings.GetOppositeAsset(uint64(sourceChainId), uint64(targetChainId), eventLog.Token.String())

	metrics.SetUserGetHisTokens(sourceChainId, targetChainId, oppositeToken, transactionId, ew.prometheusService, ew.logger)
}

func (ew *Watcher) handleBurnLog(eventLog *router.RouterBurn, q qi.Queue) {
	ew.logger.Debugf("[%s] - New Burn Event Log received.", eventLog.Raw.TxHash)

	if eventLog.Raw.Removed {
		ew.logger.Debugf("[%s] - Uncle block transaction was removed.", eventLog.Raw.TxHash)
		return
	}

	if len(eventLog.Receiver) == 0 {
		ew.logger.Errorf("[%s] - Empty receiver account.", eventLog.Raw.TxHash)
		return
	}

	var chain *big.Int
	chain, e := ew.evmClient.ChainID(context.Background())
	if e != nil {
		ew.logger.Errorf("[%s] - Failed to retrieve chain ID.", eventLog.Raw.TxHash)
		return
	}

	nativeAsset := ew.mappings.WrappedToNative(eventLog.Token.String(), chain.Int64())
	if nativeAsset == nil {
		ew.logger.Errorf("[%s] - Failed to retrieve native asset of [%s].", eventLog.Raw.TxHash, eventLog.Token)
		return
	}

	sourceChainId := chain.Int64()
	targetChainId := eventLog.TargetChain.Int64()
	transactionId := fmt.Sprintf("%s-%d", eventLog.Raw.TxHash, eventLog.Raw.Index)
	token := eventLog.Token.String()

	if ew.prometheusService.GetIsMonitoringEnabled() {
		if targetChainId != constants.HederaNetworkId {
			metrics.CreateMajorityReachedIfNotExists(sourceChainId, targetChainId, token, transactionId, ew.prometheusService, ew.logger)
		} else {
			metrics.CreateFeeTransferredIfNotExists(sourceChainId, targetChainId, token, transactionId, ew.prometheusService, ew.logger)
		}

		metrics.CreateUserGetHisTokensIfNotExists(sourceChainId, targetChainId, token, transactionId, ew.prometheusService, ew.logger)
	}

	// This is the case when you are bridging wrapped to wrapped
	if targetChainId != nativeAsset.ChainId {
		ew.logger.Errorf("[%s] - Wrapped to Wrapped transfers currently not supported [%s] - [%d] for [%d]", eventLog.Raw.TxHash, nativeAsset.Asset, nativeAsset.ChainId, eventLog.TargetChain.Int64())
		return
	}

	recipientAccount := ""
	var err error
	if targetChainId == constants.HederaNetworkId {
		recipient, err := hedera.AccountIDFromBytes(eventLog.Receiver)
		if err != nil {
			ew.logger.Errorf("[%s] - Failed to parse account from bytes [%v]. Error: [%s].", eventLog.Raw.TxHash, eventLog.Receiver, err)
			return
		}
		recipientAccount = recipient.String()
	} else {
		recipientAccount = common.BytesToAddress(eventLog.Receiver).String()
	}

	properAmount := eventLog.Amount
	if targetChainId == constants.HederaNetworkId {
		properAmount, err = ew.contracts.RemoveDecimals(properAmount, token)
		if err != nil {
			ew.logger.Errorf("[%s] - Failed to adjust [%s] amount [%s] decimals between chains.", eventLog.Raw.TxHash, eventLog.Token, properAmount)
			return
		}
	}
	if properAmount.Cmp(big.NewInt(0)) == 0 {
		ew.logger.Errorf("[%s] - Insufficient amount provided: Event Amount [%s] and Proper Amount [%s].", eventLog.Raw.TxHash, eventLog.Amount, properAmount)
		return
	}
	if properAmount.Cmp(nativeAsset.MinAmount) < 0 {
		ew.logger.Errorf("[%s] - Transfer Amount [%s] less than Minimum Amount [%s].", eventLog.Raw.TxHash, properAmount, nativeAsset.MinAmount)
		return
	}

	burnEvent := &transfer.Transfer{
		TransactionId: transactionId,
		SourceChainId: sourceChainId,
		TargetChainId: targetChainId,
		NativeChainId: nativeAsset.ChainId,
		SourceAsset:   token,
		TargetAsset:   nativeAsset.Asset,
		NativeAsset:   nativeAsset.Asset,
		Receiver:      recipientAccount,
		Amount:        properAmount.String(),
	}

	ew.logger.Infof("[%s] - New Burn Event Log with Amount [%s], Receiver Address [%s] has been found.",
		eventLog.Raw.TxHash.String(),
		eventLog.Amount.String(),
		recipientAccount)

	currentBlockNumber := eventLog.Raw.BlockNumber

	if ew.validator && currentBlockNumber >= ew.targetBlock {
		if burnEvent.TargetChainId == constants.HederaNetworkId {
			q.Push(&queue.Message{Payload: burnEvent, Topic: constants.HederaFeeTransfer})
		} else {
			q.Push(&queue.Message{Payload: burnEvent, Topic: constants.TopicMessageSubmission})
		}
	} else {
		blockTimestamp := ew.evmClient.GetBlockTimestamp(big.NewInt(int64(eventLog.Raw.BlockNumber)))

		burnEvent.Timestamp = strconv.FormatUint(blockTimestamp, 10)
		if burnEvent.TargetChainId == constants.HederaNetworkId {
			q.Push(&queue.Message{Payload: burnEvent, Topic: constants.ReadOnlyHederaTransfer})
		} else {
			q.Push(&queue.Message{Payload: burnEvent, Topic: constants.ReadOnlyTransferSave})
		}
	}
}

func (ew *Watcher) handleLockLog(eventLog *router.RouterLock, q qi.Queue) {
	ew.logger.Debugf("[%s] - New Lock Event Log received.", eventLog.Raw.TxHash)

	transactionId := fmt.Sprintf("%s-%d", eventLog.Raw.TxHash, eventLog.Raw.Index)
	targetChainId := eventLog.TargetChain.Int64()
	token := eventLog.Token.String()

	if eventLog.Raw.Removed {
		ew.logger.Errorf("[%s] - Uncle block transaction was removed.", eventLog.Raw.TxHash)
		return
	}

	if len(eventLog.Receiver) == 0 {
		ew.logger.Errorf("[%s] - Empty receiver account.", eventLog.Raw.TxHash)
		return
	}
	var chain *big.Int
	chain, e := ew.evmClient.ChainID(context.Background())
	sourceChainId := chain.Int64()
	if e != nil {
		ew.logger.Errorf("[%s] - Failed to retrieve chain ID.", eventLog.Raw.TxHash)
		return
	}

	if targetChainId != constants.HederaNetworkId {
		metrics.CreateMajorityReachedIfNotExists(sourceChainId, targetChainId, token, transactionId, ew.prometheusService, ew.logger)
	}
	metrics.CreateUserGetHisTokensIfNotExists(sourceChainId, targetChainId, token, transactionId, ew.prometheusService, ew.logger)

	recipientAccount := ""
	var err error
	if targetChainId == constants.HederaNetworkId {
		recipient, err := hedera.AccountIDFromBytes(eventLog.Receiver)
		if err != nil {
			ew.logger.Errorf("[%s] - Failed to parse account from bytes [%v]. Error: [%s].", eventLog.Raw.TxHash, eventLog.Receiver, err)
			return
		}
		recipientAccount = recipient.String()
	} else {
		recipientAccount = common.BytesToAddress(eventLog.Receiver).String()
	}

	wrappedAsset := ew.mappings.NativeToWrapped(token, sourceChainId, targetChainId)
	if wrappedAsset == "" {
		ew.logger.Errorf("[%s] - Failed to retrieve native asset of [%s].", eventLog.Raw.TxHash, eventLog.Token)
		return
	}
	nativeAsset := ew.mappings.FungibleNativeAsset(sourceChainId, token)
	if eventLog.Amount.Cmp(nativeAsset.MinAmount) < 0 {
		ew.logger.Errorf("[%s] - Transfer Amount [%s] less than Minimum Amount [%s].", eventLog.Raw.TxHash, eventLog.Amount, nativeAsset.MinAmount)
		return
	}

	properAmount := new(big.Int).Sub(eventLog.Amount, eventLog.ServiceFee)
	if targetChainId == constants.HederaNetworkId {
		properAmount, err = ew.contracts.RemoveDecimals(properAmount, token)
		if err != nil {
			ew.logger.Errorf("[%s] - Failed to adjust [%s] amount [%s] decimals between chains.", eventLog.Raw.TxHash, eventLog.Token, properAmount)
			return
		}
	}
	if properAmount.Cmp(big.NewInt(0)) == 0 {
		ew.logger.Errorf("[%s] - Insufficient amount provided: Event Amount [%s] and Proper Amount [%s].", eventLog.Raw.TxHash, eventLog.Amount, properAmount)
		return
	}

	tr := &transfer.Transfer{
		TransactionId: transactionId,
		SourceChainId: sourceChainId,
		TargetChainId: targetChainId,
		NativeChainId: sourceChainId,
		SourceAsset:   token,
		TargetAsset:   wrappedAsset,
		NativeAsset:   token,
		Receiver:      recipientAccount,
		Amount:        properAmount.String(),
	}

	ew.logger.Infof("[%s] - New Lock Event Log with Amount [%s], Receiver Address [%s], Source Chain [%d] and Target Chain [%d] has been found.",
		eventLog.Raw.TxHash.String(),
		properAmount,
		recipientAccount,
		sourceChainId,
		eventLog.TargetChain.Int64())

	currentBlockNumber := eventLog.Raw.BlockNumber

	if ew.validator && currentBlockNumber >= ew.targetBlock {
		if tr.TargetChainId == constants.HederaNetworkId {
			q.Push(&queue.Message{Payload: tr, Topic: constants.HederaMintHtsTransfer})
		} else {
			q.Push(&queue.Message{Payload: tr, Topic: constants.TopicMessageSubmission})
		}
	} else {
		blockTimestamp := ew.evmClient.GetBlockTimestamp(big.NewInt(int64(eventLog.Raw.BlockNumber)))

		tr.Timestamp = strconv.FormatUint(blockTimestamp, 10)
		if tr.TargetChainId == constants.HederaNetworkId {
			q.Push(&queue.Message{Payload: tr, Topic: constants.ReadOnlyHederaMintHtsTransfer})
		} else {
			q.Push(&queue.Message{Payload: tr, Topic: constants.ReadOnlyTransferSave})
		}
	}
}

func (ew *Watcher) handleUnlockLog(eventLog *router.RouterUnlock) {
	ew.logger.Debugf("[%s] - New Unlock Event Log received.", eventLog.Raw.TxHash)

	if eventLog.Raw.Removed {
		ew.logger.Errorf("[%s] - Uncle block transaction was removed.", eventLog.Raw.TxHash)
		return
	}

	chain, e := ew.evmClient.ChainID(context.Background())
	if e != nil {
		ew.logger.Errorf("[%s] - Failed to retrieve chain ID.", eventLog.Raw.TxHash)
		return
	}

	transactionId := string(eventLog.TransactionId)
	sourceChainId := eventLog.SourceChain.Int64()
	targetChainId := chain.Int64()
	oppositeToken := ew.mappings.GetOppositeAsset(uint64(sourceChainId), uint64(targetChainId), eventLog.Token.String())

	metrics.SetUserGetHisTokens(sourceChainId, targetChainId, oppositeToken, transactionId, ew.prometheusService, ew.logger)
}
