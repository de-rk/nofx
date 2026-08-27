export interface SystemStatus {
  trader_id: string
  trader_name: string
  ai_model: string
  is_running: boolean
  start_time: string
  runtime_minutes: number
  call_count: number
  initial_balance: number
  scan_interval: string
  stop_until: string
  last_reset_time: string
  ai_provider: string
  strategy_type?: 'ai_trading' | 'grid_trading'
  grid_symbol?: string
}

export interface AccountInfo {
  total_equity: number
  wallet_balance: number
  unrealized_profit: number // 未实现盈亏（交易所API官方值）
  available_balance: number
  total_pnl: number
  total_pnl_pct: number
  initial_balance: number
  daily_pnl: number
  position_count: number
  margin_used: number
  margin_used_pct: number
}

export interface Position {
  symbol: string
  side: string
  entry_price: number
  mark_price: number
  quantity: number
  leverage: number
  unrealized_pnl: number
  unrealized_pnl_pct: number
  liquidation_price: number
  margin_used: number
}

export interface DecisionAction {
  action: string
  symbol: string
  quantity: number
  leverage: number
  price: number
  stop_loss?: number      // Stop loss price
  take_profit?: number    // Take profit price
  confidence?: number     // AI confidence (0-100)
  reasoning?: string      // Brief reasoning
  order_id: number
  timestamp: string
  success: boolean
  error?: string
}

export interface AccountSnapshot {
  total_balance: number
  available_balance: number
  total_unrealized_profit: number
  position_count: number
  margin_used_pct: number
}

export interface DecisionRecord {
  timestamp: string
  cycle_number: number
  system_prompt: string
  input_prompt: string
  cot_trace: string
  decision_json: string
  account_state: AccountSnapshot
  positions: any[]
  candidate_coins: string[]
  decisions: DecisionAction[]
  execution_log: string[]
  success: boolean
  error_message?: string
}

export interface Statistics {
  total_cycles: number
  successful_cycles: number
  failed_cycles: number
  total_open_positions: number
  total_close_positions: number
}

// AI Trading相关类型
export interface TraderInfo {
  trader_id: string
  trader_name: string
  ai_model: string
  exchange_id?: string
  is_running?: boolean
  strategy_id?: string
  strategy_name?: string
  strategy_type?: string
  tags?: string[]
  custom_prompt?: string
  use_ai500?: boolean
  use_oi_top?: boolean
  system_prompt_template?: string
  created_at?: string
}

export interface AIModel {
  id: string
  name: string
  provider: string
  enabled: boolean
  apiKey?: string
  customApiUrl?: string
  customModelName?: string
}

export interface Exchange {
  id: string                     // UUID (empty for supported exchange templates)
  exchange_type: string          // "binance", "bybit", "okx", "hyperliquid", "aster", "lighter"
  account_name: string           // User-defined account name
  name: string                   // Display name
  type: 'cex' | 'dex'
  enabled: boolean
  apiKey?: string
  secretKey?: string
  passphrase?: string            // OKX specific
  testnet?: boolean
  // Hyperliquid specific
  hyperliquidWalletAddr?: string
  // Aster specific
  asterUser?: string
  asterSigner?: string
  asterPrivateKey?: string
  // LIGHTER specific
  lighterWalletAddr?: string
  lighterPrivateKey?: string
  lighterApiKeyPrivateKey?: string
  lighterApiKeyIndex?: number
}

export interface CreateExchangeRequest {
  exchange_type: string          // "binance", "bybit", "okx", "hyperliquid", "aster", "lighter"
  account_name: string           // User-defined account name
  enabled: boolean
  api_key?: string
  secret_key?: string
  passphrase?: string
  testnet?: boolean
  hyperliquid_wallet_addr?: string
  aster_user?: string
  aster_signer?: string
  aster_private_key?: string
  lighter_wallet_addr?: string
  lighter_private_key?: string
  lighter_api_key_private_key?: string
  lighter_api_key_index?: number
}

export interface CreateTraderRequest {
  name: string
  ai_model_id: string
  exchange_id: string
  strategy_id?: string // 策略ID（新版，使用保存的策略配置）
  initial_balance?: number // 可选：创建时由后端自动获取，编辑时可手动更新
  scan_interval_minutes?: number
  is_cross_margin?: boolean
  // 以下字段为向后兼容保留，新版使用策略配置
  btc_eth_leverage?: number
  altcoin_leverage?: number
  trading_symbols?: string
  custom_prompt?: string
  override_base_prompt?: boolean
  system_prompt_template?: string
  tags?: string[]
  use_ai500?: boolean
  use_oi_top?: boolean
}

export interface HandoffBinding {
  id: string
  source_trader_id: string
  target_trader_id: string
  enabled: boolean
  window_seconds: number
  threshold_pct: number
  cooldown_seconds: number
  state: string
  last_triggered_at?: string
  last_error?: string
}

export interface HandoffRequest {
  source_trader_id: string
  target_trader_id: string
  enabled: boolean
  window_seconds?: number
  threshold_pct?: number
  cooldown_seconds?: number
}

export interface UpdateModelConfigRequest {
  models: {
    [key: string]: {
      enabled: boolean
      api_key: string
      custom_api_url?: string
      custom_model_name?: string
    }
  }
}

export interface UpdateExchangeConfigRequest {
  exchanges: {
    [key: string]: {
      enabled: boolean
      api_key: string
      secret_key: string
      passphrase?: string
      testnet?: boolean
      // Hyperliquid 特定字段
      hyperliquid_wallet_addr?: string
      // Aster 特定字段
      aster_user?: string
      aster_signer?: string
      aster_private_key?: string
      // LIGHTER 特定字段
      lighter_wallet_addr?: string
      lighter_private_key?: string
      lighter_api_key_private_key?: string
      lighter_api_key_index?: number
    }
  }
}

// Competition related types
export interface CompetitionTraderData {
  trader_id: string
  trader_name: string
  ai_model: string
  exchange: string
  total_equity: number
  total_pnl: number
  total_pnl_pct: number
  position_count: number
  margin_used_pct: number
  is_running: boolean
}

export interface CompetitionData {
  traders: CompetitionTraderData[]
  count: number
}

// Trader Configuration Data for View Modal
export interface TraderConfigData {
  trader_id?: string
  trader_name: string
  ai_model: string
  exchange_id: string
  strategy_id?: string  // 策略ID
  strategy_name?: string  // 策略名称
  strategy_type?: string
  trend_gate?: TrendGateConfig
  is_cross_margin: boolean
  scan_interval_minutes: number
  initial_balance: number
  is_running: boolean
  // 以下为旧版字段（向后兼容）
  btc_eth_leverage?: number
  altcoin_leverage?: number
  trading_symbols?: string
  custom_prompt?: string
  override_base_prompt?: boolean
  system_prompt_template?: string
  tags?: string[]
  use_ai500?: boolean
  use_oi_top?: boolean
}

// Strategy Studio Types
export interface Strategy {
  id: string;
  name: string;
  description: string;
  is_active: boolean;
  is_default: boolean;
  is_public: boolean;           // 是否在策略市场公开
  config_visible: boolean;      // 配置参数是否公开可见
  config: StrategyConfig;
  created_at: string;
  updated_at: string;
}

// 策略使用统计
export interface StrategyStats {
  clone_count: number;          // 被克隆次数
  active_users: number;         // 当前使用人数
  top_performers?: StrategyPerformer[];  // 收益排行
}

// 策略使用者收益排行
export interface StrategyPerformer {
  user_id: string;
  user_name: string;            // 脱敏后的用户名
  total_pnl_pct: number;        // 总收益率
  total_pnl: number;            // 总收益金额
  win_rate: number;             // 胜率
  trade_count: number;          // 交易次数
  using_since: string;          // 使用开始时间
  rank: number;                 // 排名
}

export interface PromptSectionsConfig {
  role_definition?: string;
  trading_frequency?: string;
  entry_standards?: string;
  decision_process?: string;
}

export interface StrategyConfig {
  // Strategy type: "ai_trading" (default) or "grid_trading"
  strategy_type?: 'ai_trading' | 'grid_trading';
  // Language setting: "zh" for Chinese, "en" for English
  // Determines the language used for data formatting and prompt generation
  language?: 'zh' | 'en';
  coin_source: CoinSourceConfig;
  indicators: IndicatorConfig;
  custom_prompt?: string;
  risk_control: RiskControlConfig;
  prompt_sections?: PromptSectionsConfig;
  // Grid trading configuration (only used when strategy_type is 'grid_trading')
  grid_config?: GridStrategyConfig;
  trend_gate?: TrendGateConfig;
}

// Grid trading specific configuration
export interface GridStrategyConfig {
  // Trading pair (e.g., "BTCUSDT")
  symbol: string;
  // Number of grid levels (5-50)
  grid_count: number;
  // Total investment in USDT
  total_investment: number;
  // Leverage (1-20)
  leverage: number;
  // Upper price boundary (0 = auto-calculate from ATR)
  upper_price: number;
  // Lower price boundary (0 = auto-calculate from ATR)
  lower_price: number;
  // Use ATR to auto-calculate bounds
  use_atr_bounds: boolean;
  // ATR multiplier for bound calculation (default 2.0)
  atr_multiplier: number;
  // Position distribution: "uniform" | "gaussian" | "pyramid"
  distribution: 'uniform' | 'gaussian' | 'pyramid';
  // Use maker-only orders for lower fees
  use_maker_only: boolean;
  // Auto-close small positions (value < 100 USDT) when profit exceeds step*1.2 (default true)
  enable_small_position_close?: boolean;
  // Profit drawdown threshold for auto close (default 50%)
  profit_drawdown_threshold?: number;
  // Enable AI batch position reduction when trapped (被套时分批减仓)
  enable_trapped_reduce?: boolean;
  // Unrealized loss % to trigger batch reduction (default 3.0%)
  trapped_reduce_threshold_pct?: number;
  t_trade_position_threshold_pct?: number;
  // Enable profit-based position reduction (盈利分批减仓, default true)
  enable_profit_reduce?: boolean;
  // Profit reduce step percentage (default 10%)
  profit_reduce_step_pct?: number;
  // Profit reduce amount multiplier (0.5-2.0, default 1.0)
  profit_reduce_multiplier?: number;
  // Minimum spread % for T-trade reduce orders (default 0.2, range 0.2-1.0)
  t_trade_spread_pct?: number;
  // Periodically refresh total investment from wallet balance
  enable_investment_refresh?: boolean;
  // Refresh interval in days (default 2)
  investment_refresh_days?: number;
  // Kline timeframe that triggers AI grid cycle on candle close (default "5m")
  ai_trigger_tf?: string;
  // Decision mode: "ai" | "ai_with_algo_fallback" | "algo_only". Unset/empty defaults to "ai".
  decision_mode?: 'ai' | 'ai_with_algo_fallback' | 'algo_only';
}

export interface CoinSourceConfig {
  source_type: 'static' | 'ai500' | 'oi_top' | 'oi_low' | 'mixed';
  // Used directly in static mode, as AI500 fallback, or as a mixed source.
  static_coins?: string[];
  excluded_coins?: string[];   // 排除的币种列表
  use_ai500: boolean;
  ai500_limit?: number;
  use_oi_top: boolean;
  oi_top_limit?: number;
  use_oi_low: boolean;
  oi_low_limit?: number;
  // Note: API URLs are now built automatically using nofxos_api_key from IndicatorConfig
}

export interface IndicatorConfig {
  klines: KlineConfig;
  // Raw OHLCV kline data - required for AI analysis
  enable_raw_klines: boolean;
  // Technical indicators (optional)
  enable_ema: boolean;
  enable_macd: boolean;
  enable_rsi: boolean;
  enable_atr: boolean;
  enable_boll: boolean;
  enable_volume: boolean;
  enable_oi: boolean;
  enable_funding_rate: boolean;
  ema_periods?: number[];
  rsi_periods?: number[];
  atr_periods?: number[];
  boll_periods?: number[];
  external_data_sources?: ExternalDataSource[];

  // ========== NofxOS 数据源统一配置 ==========
  // Unified NofxOS API Key - used for all NofxOS data sources
  nofxos_api_key?: string;

  // 量化数据源（资金流向、持仓变化、价格变化）
  enable_quant_data?: boolean;
  enable_quant_oi?: boolean;
  enable_quant_netflow?: boolean;

  // OI 排行数据（市场持仓量增减排行）
  enable_oi_ranking?: boolean;
  oi_ranking_duration?: string;  // "1h", "4h", "24h"
  oi_ranking_limit?: number;

  // NetFlow 排行数据（机构/散户资金流向排行）
  enable_netflow_ranking?: boolean;
  netflow_ranking_duration?: string;  // "1h", "4h", "24h"
  netflow_ranking_limit?: number;

  // Price 排行数据（涨跌幅排行）
  enable_price_ranking?: boolean;
  price_ranking_duration?: string;  // "1h", "4h", "24h" or "1h,4h,24h"
  price_ranking_limit?: number;
}

export interface KlineConfig {
  primary_timeframe: string;
  primary_count: number;
  longer_timeframe?: string;
  longer_count?: number;
  enable_multi_timeframe: boolean;
  // 新增：支持选择多个时间周期
  selected_timeframes?: string[];
}

export interface ExternalDataSource {
  name: string;
  type: 'api' | 'webhook';
  url: string;
  method: string;
  headers?: Record<string, string>;
  data_path?: string;
  refresh_secs?: number;
}

export interface TrendGateConfig {
  enabled: boolean
  timeframe?: string
  lookback?: number
  min_price_change_pct?: number
  min_volume_ratio?: number
}

export interface RiskControlConfig {
  // Allow AI to configure a native exchange trailing stop when supported
  enable_trailing_stop?: boolean;
  // Max number of coins held simultaneously (CODE ENFORCED)
  max_positions: number;

  // Trading Leverage - exchange leverage for opening positions (AI guided)
  btc_eth_max_leverage: number;    // BTC/ETH max exchange leverage
  altcoin_max_leverage: number;    // Altcoin max exchange leverage

  // Position Value Ratio - single position notional value / account equity (CODE ENFORCED)
  // Max position value = equity × this ratio
  btc_eth_max_position_value_ratio?: number;     // default: 5 (BTC/ETH max position = 5x equity)
  altcoin_max_position_value_ratio?: number;     // default: 1 (Altcoin max position = 1x equity)

  // Risk Parameters
  max_margin_usage: number;        // Max margin utilization, e.g. 0.9 = 90% (CODE ENFORCED)
  min_position_size: number;       // Min position size in USDT (CODE ENFORCED)
  min_risk_reward_ratio: number;   // Min take_profit / stop_loss ratio (AI guided)
  min_confidence: number;          // Min AI confidence to open position (AI guided)

  // Profit Drawdown Protection
  profit_drawdown_pct?: number;    // Profit drawdown threshold (%) - auto close when exceeded
  profit_threshold_pct?: number;   // Profit threshold (%) - drawdown protection triggers when profit exceeds this
}

// Position History Types
export interface HistoricalPosition {
  id: number;
  trader_id: string;
  exchange_id: string;
  exchange_type: string;
  symbol: string;
  side: string;
  quantity: number;
  entry_quantity: number;
  entry_price: number;
  entry_order_id: string;
  entry_time: string;
  exit_price: number;
  exit_order_id: string;
  exit_time: string;
  realized_pnl: number;
  fee: number;
  leverage: number;
  status: string;
  close_reason: string;
  created_at: string;
  updated_at: string;
}

// Matches Go TraderStats struct exactly
export interface TraderStats {
  total_trades: number;
  win_trades: number;
  loss_trades: number;
  win_rate: number;
  profit_factor: number;
  sharpe_ratio: number;
  total_pnl: number;
  total_fee: number;
  avg_win: number;
  avg_loss: number;
  max_drawdown_pct: number;
}

// Matches Go SymbolStats struct exactly
export interface SymbolStats {
  symbol: string;
  total_trades: number;
  win_trades: number;
  win_rate: number;
  total_pnl: number;
  avg_pnl: number;
  avg_hold_mins: number;
}

// Matches Go DirectionStats struct exactly
export interface DirectionStats {
  side: string;
  trade_count: number;
  win_rate: number;
  total_pnl: number;
  avg_pnl: number;
}

export interface PositionHistoryResponse {
  positions: HistoricalPosition[];
  stats: TraderStats | null;
  symbol_stats: SymbolStats[];
  direction_stats: DirectionStats[];
}

// Grid Risk Information for frontend display
export interface GridRiskInfo {
  // Leverage info
  current_leverage: number
  effective_leverage: number
  recommended_leverage: number

  // Position info
  current_position: number
  max_position: number
  position_percent: number

  // Liquidation info
  liquidation_price: number
  liquidation_distance: number

  // Market state
  regime_level: string

  // Box state
  short_box_upper: number
  short_box_lower: number
  mid_box_upper: number
  mid_box_lower: number
  long_box_upper: number
  long_box_lower: number
  current_price: number

  // Breakout state
  breakout_level: string
  breakout_direction: string

  // Grid direction
  // Profit reduce tracker
  long_profit_reduced_pct: number
  short_profit_reduced_pct: number
  profit_reduce_step: number
}

export interface GridTradeLog {
  id: number
  instance_id: string
  created_at: string
  source: string       // "ai" | "algo" | "ttrade" | "profit_reduce"
  action: string       // "hold" | "reduce_long" | "close_long" | "ttrade_tag" | "ttrade_fill" | "profit_reduce" | "profit_reduce_close" | ...
  symbol: string
  side: string         // "long" | "short"
  quantity: number
  price: number
  entry_price: number
  mark_price: number
  margin_profit: number
  unrealized_pl: number
  reason: string
  order_id: string
  success: boolean
  error_msg: string
}
