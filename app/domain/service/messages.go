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

package service

import (
	"github.com/limechain/hedera-eth-bridge-validator/app/model/message"
)

type Messages interface {
	// SanityCheckSignature performs any validation required prior handling the topic message
	// (verifies metadata against the corresponding Transaction record)
	SanityCheckSignature(tm message.Message) (bool, error)
	// VerifyEthereumTxAuthenticity performs the validation required prior handling the topic message
	// (verifies the submitted TX against the required target contract and arguments passed)
	VerifyEthereumTxAuthenticity(tm message.Message) (bool, error)
	// ProcessSignature processes the signature message, verifying and updating all necessary fields in the DB
	ProcessSignature(tm message.Message) error
	// ScheduleForSubmission computes the execution slot and schedules the Ethereum Mint TX for submission
	ScheduleEthereumTxForSubmission(transferID string) error
	// ProcessEthereumTxMessage
	ProcessEthereumTxMessage(tm message.Message) error
}
