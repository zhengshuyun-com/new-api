/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Central export point for all lib utilities
 */

// Format utilities (usage-logs specific)
export {
  parseLogOther,
  getTimeColor,
  formatModelName,
  formatDuration,
  getParamOverrideActionLabel,
  parseAuditLine,
  isViolationFeeLog,
} from './format'

// Filter utilities
export { buildSearchParams, getLogCategoryLabel } from './filter'

// General utilities
export {
  isDisplayableLogType,
  isTimingLogType,
  getLogTypeConfig,
  isPerCallBilling,
  getDefaultTimeRange,
  buildBaseParams,
  buildApiParams,
  fetchLogsByCategory,
} from './utils'

// buildQueryParams lives in ../api to avoid an import cycle between api.ts and
// lib/utils.ts; re-export it from the same public surface as before.
export { buildQueryParams } from '../api'

// Status mapper utilities
export { createStatusMapper } from './status'

// Mappers
export {
  mjTaskTypeMapper,
  mjStatusMapper,
  taskActionMapper,
  taskStatusMapper,
  taskPlatformMapper,
} from './mappers'

// Column utilities
export { useColumnsByCategory } from './columns'
