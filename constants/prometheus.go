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

package constants

// Prometheus metrics
const (
	ValidatorsParticipationRateName = "validators_participation_rate"
	ValidatorsParticipationRateHelp = "Participation rate: Track validators' activity in %."
	FeeAccountAmountName            = "fee_account_amount"
	FeeAccountAmountHelp            = "Fee account amount."
	BridgeAccountAmountName         = "bridge_account_amount"
	BridgeAccountAmountHelp         = "Bridge account amount."
	OperatorAccountAmountName       = "operator_account_amount"
	OperatorAccountAmountHelp       = "Operator account amount."
	DotSymbol                       = "." //not fit prometheus validation https://github.com/prometheus/common/blob/main/model/metric.go#L97
	ReplaceDotSymbol                = "_"
	DotSymbolRep                    = 2
	AssetMetricsNamePrefix          = "token_id_"
	AssetMetricsHelpPrefix          = "The total supply of "
	BridgeAssetMetricsNamePrefix    = "bridge_acc_"
	BridgeAssetMetricsNameHelp      = "Bridge account balance for "
	BalanceAssetMetricNamePrefix    = "balance_"
	BalanceAssetMetricHelpPrefix    = "The balance of "
	AssetMetricHelpSuffix           = " at router address "
	CountAssetMetricNamePrefix      = "count_"
	CountAssetMetricHelpPrefix      = "The count of "
)
