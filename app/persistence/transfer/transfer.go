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

package transfer

import (
	"errors"
	"github.com/limechain/hedera-eth-bridge-validator/app/persistence/entity"
	"github.com/limechain/hedera-eth-bridge-validator/app/persistence/entity/transfer"
	"github.com/limechain/hedera-eth-bridge-validator/config"
	"github.com/limechain/hedera-eth-bridge-validator/proto"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Repository struct {
	dbClient *gorm.DB
	logger   *log.Entry
}

func NewRepository(dbClient *gorm.DB) *Repository {
	return &Repository{
		dbClient: dbClient,
		logger:   config.GetLoggerFor("Transfer Repository"),
	}
}

func (tr Repository) GetByTransactionId(txId string) (*entity.Transfer, error) {
	tx := &entity.Transfer{}
	result := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		First(tx)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, result.Error
	}
	return tx, nil
}

func (tr Repository) GetWithMessages(txId string) (*entity.Transfer, error) {
	tx := &entity.Transfer{}
	err := tr.dbClient.
		Preload("Messages").
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		Find(tx).Error
	return tx, err
}

func (tr Repository) GetInitialAndSignatureSubmittedTx() ([]*entity.Transfer, error) {
	var transfers []*entity.Transfer

	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("status = ? OR status = ?", transfer.StatusInitial, transfer.StatusSignatureSubmitted).
		Find(&transfers).Error
	if err != nil {
		return nil, err
	}

	return transfers, nil
}

// Create creates new record of Transfer
func (tr Repository) Create(ct *proto.TransferMessage) (*entity.Transfer, error) {
	return tr.create(ct, transfer.StatusInitial)
}

// Save updates the provided Transfer instance
func (tr Repository) Save(tx *entity.Transfer) error {
	return tr.dbClient.Save(tx).Error
}

func (tr *Repository) SaveRecoveredTxn(ct *proto.TransferMessage) error {
	_, err := tr.create(ct, transfer.StatusRecovered)
	return err
}

func (tr Repository) UpdateStatusInsufficientFee(txId string) error {
	return tr.updateStatus(txId, transfer.StatusInsufficientFee)
}

func (tr Repository) UpdateStatusCompleted(txId string) error {
	return tr.updateStatus(txId, transfer.StatusCompleted)
}

func (tr Repository) UpdateStatusSignatureSubmitted(txId string) error {
	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		Updates(entity.Transfer{SignatureMsgStatus: transfer.StatusSignatureSubmitted, Status: transfer.StatusInProgress}).
		Error
	if err == nil {
		tr.logger.Debugf("[%s] - Updated Status to [%s] and SignatureMsgStatus to [%s]", txId, transfer.StatusInProgress, transfer.StatusSignatureSubmitted)
	}
	return err
}

func (tr Repository) UpdateStatusSignatureMined(txId string) error {
	return tr.updateSignatureStatus(txId, transfer.StatusSignatureMined)
}

func (tr Repository) UpdateStatusSignatureFailed(txId string) error {
	return tr.updateSignatureStatus(txId, transfer.StatusSignatureFailed)
}

func (tr Repository) UpdateEthTxSubmitted(txId string, hash string) error {
	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		Updates(entity.Transfer{EthTxStatus: transfer.StatusEthTxSubmitted, EthTxHash: hash}).
		Error
	if err == nil {
		tr.logger.Debugf("[%s] - Updated Ethereum TX Status to [%s]", txId, transfer.StatusEthTxSubmitted)
	}
	return err
}

func (tr Repository) UpdateEthTxMined(txId string) error {
	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		Updates(entity.Transfer{EthTxStatus: transfer.StatusEthTxMined, Status: transfer.StatusCompleted}).
		Error
	if err == nil {
		tr.logger.Debugf("[%s] - Updated Ethereum TX Status to [%s] and Transfer status to [%s]", txId, transfer.StatusEthTxMined, transfer.StatusCompleted)
	}
	return err
}

func (tr Repository) UpdateEthTxReverted(txId string) error {
	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		Updates(entity.Transfer{EthTxStatus: transfer.StatusEthTxReverted, Status: transfer.StatusFailed}).
		Error
	if err == nil {
		tr.logger.Debugf("Updated Ethereum TX Status of TX [%s] to [%s] and Transfer status to [%s]", txId, transfer.StatusEthTxReverted, transfer.StatusFailed)
	}
	return err
}

func (tr Repository) UpdateStatusEthTxMsgSubmitted(txId string) error {
	return tr.updateEthereumTxMsgStatus(txId, transfer.StatusEthTxMsgSubmitted)
}

func (tr Repository) UpdateStatusEthTxMsgMined(txId string) error {
	return tr.updateEthereumTxMsgStatus(txId, transfer.StatusEthTxMsgMined)
}

func (tr Repository) UpdateStatusEthTxMsgFailed(txId string) error {
	return tr.updateEthereumTxMsgStatus(txId, transfer.StatusEthTxMsgFailed)
}

func (tr Repository) create(ct *proto.TransferMessage, status string) (*entity.Transfer, error) {
	tx := &entity.Transfer{
		TransactionID:         ct.TransactionId,
		Receiver:              ct.Receiver,
		Amount:                ct.Amount,
		TxReimbursement:       ct.TxReimbursement,
		Status:                status,
		SourceAsset:           ct.SourceAsset,
		TargetAsset:           ct.TargetAsset,
		GasPrice:              ct.GasPrice,
		ExecuteEthTransaction: ct.ExecuteEthTransaction,
	}
	err := tr.dbClient.Create(tx).Error

	return tx, err
}

func (tr Repository) updateStatus(txId string, status string) error {
	// Sanity check
	if status != transfer.StatusInitial &&
		status != transfer.StatusInsufficientFee &&
		status != transfer.StatusInProgress &&
		status != transfer.StatusCompleted {
		return errors.New("invalid signature status")
	}

	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		UpdateColumn("status", status).
		Error
	if err == nil {
		tr.logger.Debugf("Updated Status of TX [%s] to [%s]", txId, status)
	}
	return err
}

func (tr Repository) updateSignatureStatus(txId string, status string) error {
	return tr.baseUpdateStatus("signature_msg_status", txId, status, []string{transfer.StatusSignatureSubmitted, transfer.StatusSignatureMined, transfer.StatusSignatureFailed})
}

func (tr Repository) updateEthereumTxStatus(txId string, status string) error {
	return tr.baseUpdateStatus("eth_tx_status", txId, status, []string{transfer.StatusEthTxSubmitted, transfer.StatusEthTxMined, transfer.StatusEthTxReverted})
}

func (tr Repository) updateEthereumTxMsgStatus(txId string, status string) error {
	return tr.baseUpdateStatus("eth_tx_msg_status", txId, status, []string{transfer.StatusEthTxMsgSubmitted, transfer.StatusEthTxMsgMined, transfer.StatusEthTxMsgFailed})
}

func (tr Repository) baseUpdateStatus(statusColumn, txId, status string, possibleStatuses []string) error {
	if !isValidStatus(status, possibleStatuses) {
		return errors.New("invalid status")
	}

	err := tr.dbClient.
		Model(entity.Transfer{}).
		Where("transaction_id = ?", txId).
		UpdateColumn(statusColumn, status).
		Error
	if err == nil {
		tr.logger.Debugf("[%s] - Column [%s] status to [%s]", txId, statusColumn, status)
	}
	return err
}

func isValidStatus(status string, possibleStatuses []string) bool {
	for _, option := range possibleStatuses {
		if status == option {
			return true
		}
	}
	return false
}

func (tr *Repository) GetUnprocessedTransfers() ([]*entity.Transfer, error) {
	var transfers []*entity.Transfer

	err := tr.dbClient.
		Where("status IN ?", []string{transfer.StatusInitial, transfer.StatusRecovered}).
		Find(&transfers).Error
	if err != nil {
		return nil, err
	}

	return transfers, nil
}