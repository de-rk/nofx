import { Grid, DollarSign, TrendingUp, Shield } from 'lucide-react'
import type { GridStrategyConfig } from '../../types'

interface GridConfigEditorProps {
  config: GridStrategyConfig
  onChange: (config: GridStrategyConfig) => void
  disabled?: boolean
  language: string
}

// Default grid config
export const defaultGridConfig: GridStrategyConfig = {
  symbol: 'BTCUSDT',
  grid_count: 10,
  total_investment: 1000,
  leverage: 5,
  upper_price: 0,
  lower_price: 0,
  use_atr_bounds: true,
  atr_multiplier: 2.0,
  distribution: 'gaussian',
  use_maker_only: true,
  profit_drawdown_threshold: 50,
  enable_trapped_reduce: false,
  trapped_reduce_threshold_pct: 3.0,
  t_trade_position_threshold_pct: 30,
  enable_profit_reduce: true,
  profit_reduce_step_pct: 10,
  hedge_lock_threshold_pct: 0,
}

export function GridConfigEditor({
  config,
  onChange,
  disabled,
  language,
}: GridConfigEditorProps) {
  const t = (key: string) => {
    const translations: Record<string, Record<string, string>> = {
      // Section titles
      tradingPair: { zh: '交易设置', en: 'Trading Setup' },
      gridParameters: { zh: '网格参数', en: 'Grid Parameters' },
      priceBounds: { zh: '价格边界', en: 'Price Bounds' },
      riskControl: { zh: '风险控制', en: 'Risk Control' },

      // Trading pair
      symbol: { zh: '交易对', en: 'Trading Pair' },
      symbolDesc: { zh: '选择要进行网格交易的交易对', en: 'Select trading pair for grid trading' },

      // Investment
      totalInvestment: { zh: '投资金额 (USDT)', en: 'Investment (USDT)' },
      totalInvestmentDesc: { zh: '跟随实际账户余额，运行时自动获取', en: 'Follows actual account balance at runtime' },
      leverage: { zh: '杠杆倍数', en: 'Leverage' },
      leverageDesc: { zh: '交易使用的杠杆倍数 (1-5)', en: 'Leverage for trading (1-5)' },

      // Grid parameters
      gridCount: { zh: '网格数量', en: 'Grid Count' },
      gridCountDesc: { zh: '网格层级数量 (5-50)', en: 'Number of grid levels (5-50)' },
      distribution: { zh: '资金分配方式', en: 'Distribution' },
      distributionDesc: { zh: '网格层级的资金分配方式', en: 'Fund allocation across grid levels' },
      uniform: { zh: '均匀分配', en: 'Uniform' },
      gaussian: { zh: '高斯分配 (推荐)', en: 'Gaussian (Recommended)' },
      pyramid: { zh: '金字塔分配', en: 'Pyramid' },

      // Price bounds
      useAtrBounds: { zh: '自动计算边界 (ATR)', en: 'Auto-calculate Bounds (ATR)' },
      useAtrBoundsDesc: { zh: '基于 ATR 自动计算网格上下边界', en: 'Auto-calculate bounds based on ATR' },
      atrMultiplier: { zh: 'ATR 倍数', en: 'ATR Multiplier' },
      atrMultiplierDesc: { zh: '边界距离当前价格的 ATR 倍数', en: 'ATR multiplier for bounds distance' },
      upperPrice: { zh: '上边界价格', en: 'Upper Price' },
      upperPriceDesc: { zh: '网格上边界价格 (0=自动计算)', en: 'Grid upper bound (0=auto)' },
      lowerPrice: { zh: '下边界价格', en: 'Lower Price' },
      lowerPriceDesc: { zh: '网格下边界价格 (0=自动计算)', en: 'Grid lower bound (0=auto)' },

      // Risk control
      profitDrawdown: { zh: '利润回撤阈值 (%)', en: 'Profit Drawdown (%)' },
      profitDrawdownDesc: { zh: '盈利回撤超过此值时自动平仓 (当利润>5%时)', en: 'Auto close when profit drawdown exceeds this (when profit>5%)' },
      useMakerOnly: { zh: '仅使用 Maker 订单', en: 'Maker Only Orders' },
      useMakerOnlyDesc: { zh: '使用限价单以降低手续费', en: 'Use limit orders for lower fees' },

      // T-trade section (combines profit reduce + trapped reduce)
      tTradeSection: { zh: 'T字操作', en: 'T-Trade Operations' },

      // Profit reduce
      profitReduce: { zh: 'AI盈利减仓', en: 'AI Profit Reduction' },
      enableProfitReduce: { zh: '启用盈利减仓', en: 'Enable Profit Reduction' },
      enableProfitReduceDesc: { zh: '盈利达到触发步长时自动减仓，每多盈利一个步长再减一次，比例递增，锁定利润防止回撤', en: 'Auto-reduce when profit hits the step threshold, repeat at each increment with increasing ratio' },
      profitReduceExplain: { zh: '💡 盈利减仓规则：每盈利N%减仓N%，下一档再减2N%，以此类推（基于保证金收益率，每次减剩余仓位）', en: '💡 Profit reduce: at N% profit reduce N%, at 2N% reduce 2N% of remaining, etc. (margin-based)' },
      profitReduceStep: { zh: '触发步长 (%)', en: 'Step Size (%)' },
      profitReduceStepDesc: { zh: '每隔多少%盈利触发一次减仓（默认10%）', en: 'Profit increment that triggers each reduction (default 10%)' },

      // Trapped reduce
      trappedReduce: { zh: 'AI减仓 (T字操作)', en: 'AI Position Reduce (T-Trade)' },
      enableTrappedReduce: { zh: '启用T字操作', en: 'Enable T-Trade' },
      enableTrappedReduceDesc: { zh: '仓位超过阈值时自动T字操作，等网格单成交后差价减仓', en: 'Auto T-trade when position exceeds threshold — reduce at spread after grid order fills' },
      tTradePositionThreshold: { zh: 'T字触发仓位 (%)', en: 'T-Trade Position Threshold (%)' },
      tTradePositionThresholdDesc: { zh: '任一方向仓位占总资金超过此比例时启用T字操作（默认30%）', en: 'Enable T-trade when either side position exceeds this % of total investment (default 30%)' },
      trappedReduceThreshold: { zh: '对冲锁仓触发阈值 (%)', en: 'Hedge Lock Loss Threshold (%)' },
      trappedReduceThresholdDesc: { zh: '未实现亏损达到此值时触发对冲锁仓（hedge_lock_threshold_pct）', en: 'Unrealized loss threshold for hedge lock (see hedge_lock_threshold_pct)' },
      trappedReduceExplain: { zh: '💡 T字操作原理：仓位超阈值时，等待最近网格挂单成交，再在更优价格减仓，利用价差降低持仓成本', en: '💡 T-Trade: when position exceeds threshold, wait for nearest grid order to fill, then reduce at a better price to capture the spread' },
      tTradeSpread: { zh: 'T字差价 (%)', en: 'T-Trade Spread (%)' },
      tTradeSpreadDesc: { zh: '减仓限价单与触发单成交价的最小差价百分比（0.2%~1%）', en: 'Minimum spread % between reduce limit price and prep fill price (0.2%–1%)' },
      hedgeLockSection: { zh: '对冲锁仓', en: 'Hedge Lock' },
      hedgeLockThreshold: { zh: '触发阈值 (%)', en: 'Trigger Threshold (%)' },
      hedgeLockThresholdDesc: { zh: '被套方向亏损达到此百分比时自动开对冲单（0 = 禁用）', en: 'Open hedge when trapped loss reaches this % (0 = disabled)' },
    }
    return translations[key]?.[language] || key
  }

  const updateField = <K extends keyof GridStrategyConfig>(
    key: K,
    value: GridStrategyConfig[K]
  ) => {
    if (!disabled) {
      onChange({ ...config, [key]: value })
    }
  }

  const inputStyle = {
    background: '#1E2329',
    border: '1px solid #2B3139',
    color: '#EAECEF',
  }

  const sectionStyle = {
    background: '#0B0E11',
    border: '1px solid #2B3139',
  }

  return (
    <div className="space-y-6">
      {/* Trading Setup */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <DollarSign className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('tradingPair')}
          </h3>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {/* Symbol */}
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('symbol')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('symbolDesc')}
            </p>
            <select
              value={config.symbol}
              onChange={(e) => updateField('symbol', e.target.value)}
              disabled={disabled}
              className="w-full px-3 py-2 rounded"
              style={inputStyle}
            >
              <option value="BTCUSDT">BTC/USDT</option>
              <option value="ETHUSDT">ETH/USDT</option>
              <option value="SOLUSDT">SOL/USDT</option>
              <option value="BNBUSDT">BNB/USDT</option>
              <option value="XRPUSDT">XRP/USDT</option>
              <option value="DOGEUSDT">DOGE/USDT</option>
              <option value="HYPEUSDT">HYPE/USDT</option>
            </select>
          </div>

          {/* Investment - read-only, follows account balance */}
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('totalInvestment')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('totalInvestmentDesc')}
            </p>
            <div
              className="w-full px-3 py-2 rounded text-sm"
              style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#848E9C' }}
            >
              {language === 'zh' ? '自动 (账户余额)' : 'Auto (Account Balance)'}
            </div>
          </div>

          {/* Leverage */}
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('leverage')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('leverageDesc')}
            </p>
            <input
              type="number"
              value={config.leverage}
              onChange={(e) => updateField('leverage', parseInt(e.target.value) || 5)}
              disabled={disabled}
              min={1}
              max={5}
              className="w-full px-3 py-2 rounded"
              style={inputStyle}
            />
          </div>
        </div>
      </div>

      {/* Grid Parameters */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <Grid className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('gridParameters')}
          </h3>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Grid Count */}
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('gridCount')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('gridCountDesc')}
            </p>
            <input
              type="number"
              value={config.grid_count}
              onChange={(e) => updateField('grid_count', parseInt(e.target.value) || 10)}
              disabled={disabled}
              min={5}
              max={50}
              className="w-full px-3 py-2 rounded"
              style={inputStyle}
            />
          </div>

          {/* Distribution */}
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('distribution')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('distributionDesc')}
            </p>
            <select
              value={config.distribution}
              onChange={(e) => updateField('distribution', e.target.value as 'uniform' | 'gaussian' | 'pyramid')}
              disabled={disabled}
              className="w-full px-3 py-2 rounded"
              style={inputStyle}
            >
              <option value="uniform">{t('uniform')}</option>
              <option value="gaussian">{t('gaussian')}</option>
              <option value="pyramid">{t('pyramid')}</option>
            </select>
          </div>
        </div>
      </div>

      {/* Price Bounds */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <TrendingUp className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('priceBounds')}
          </h3>
        </div>

        {/* ATR Toggle */}
        <div className="p-4 rounded-lg mb-4" style={sectionStyle}>
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm" style={{ color: '#EAECEF' }}>
                {t('useAtrBounds')}
              </label>
              <p className="text-xs" style={{ color: '#848E9C' }}>
                {t('useAtrBoundsDesc')}
              </p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={config.use_atr_bounds}
                onChange={(e) => updateField('use_atr_bounds', e.target.checked)}
                disabled={disabled}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-gray-600 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full rtl:peer-checked:after:-translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:start-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-[#F0B90B]"></div>
            </label>
          </div>
        </div>

        {config.use_atr_bounds ? (
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('atrMultiplier')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('atrMultiplierDesc')}
            </p>
            <input
              type="number"
              value={config.atr_multiplier}
              onChange={(e) => updateField('atr_multiplier', parseFloat(e.target.value) || 2.0)}
              disabled={disabled}
              min={1}
              max={5}
              step={0.5}
              className="w-32 px-3 py-2 rounded"
              style={inputStyle}
            />
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="p-4 rounded-lg" style={sectionStyle}>
              <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
                {t('upperPrice')}
              </label>
              <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
                {t('upperPriceDesc')}
              </p>
              <input
                type="number"
                value={config.upper_price}
                onChange={(e) => updateField('upper_price', parseFloat(e.target.value) || 0)}
                disabled={disabled}
                min={0}
                step={0.01}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
            </div>
            <div className="p-4 rounded-lg" style={sectionStyle}>
              <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
                {t('lowerPrice')}
              </label>
              <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
                {t('lowerPriceDesc')}
              </p>
              <input
                type="number"
                value={config.lower_price}
                onChange={(e) => updateField('lower_price', parseFloat(e.target.value) || 0)}
                disabled={disabled}
                min={0}
                step={0.01}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
            </div>
          </div>
        )}
      </div>

      {/* Risk Control */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <Shield className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('riskControl')}
          </h3>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
          <div className="p-4 rounded-lg" style={sectionStyle}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {t('profitDrawdown')}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {t('profitDrawdownDesc')}
            </p>
            <input
              type="number"
              value={config.profit_drawdown_threshold ?? 50}
              onChange={(e) => updateField('profit_drawdown_threshold', parseFloat(e.target.value) || 50)}
              disabled={disabled}
              min={20}
              max={80}
              className="w-full px-3 py-2 rounded"
              style={inputStyle}
            />
          </div>
        </div>

        {/* Maker Only Toggle */}
        <div className="p-4 rounded-lg" style={sectionStyle}>
          <div className="flex items-center justify-between">
            <div>
              <label className="block text-sm" style={{ color: '#EAECEF' }}>
                {t('useMakerOnly')}
              </label>
              <p className="text-xs" style={{ color: '#848E9C' }}>
                {t('useMakerOnlyDesc')}
              </p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={config.use_maker_only}
                onChange={(e) => updateField('use_maker_only', e.target.checked)}
                disabled={disabled}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-gray-600 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full rtl:peer-checked:after:-translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:start-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-[#F0B90B]"></div>
            </label>
          </div>
        </div>
      </div>

      {/* ===== T字操作 Section (Profit Reduce + Trapped Reduce) ===== */}
      <div className="p-4 rounded-lg" style={{ background: '#1A1D23', border: '1px solid #2B3139' }}>
        <div className="flex items-center gap-2 mb-4">
          <TrendingUp className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('tTradeSection')}
          </h3>
        </div>

        {/* Profit Reduce */}
        <div className="mb-4">
          <p className="text-xs mb-3 px-1" style={{ color: '#848E9C' }}>{t('profitReduceExplain')}</p>
          <div className="p-4 rounded-lg mb-3" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
            <div className="flex items-center justify-between">
              <div>
                <label className="block text-sm" style={{ color: '#EAECEF' }}>{t('enableProfitReduce')}</label>
                <p className="text-xs" style={{ color: '#848E9C' }}>{t('enableProfitReduceDesc')}</p>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input
                  type="checkbox"
                  checked={config.enable_profit_reduce ?? true}
                  onChange={(e) => updateField('enable_profit_reduce', e.target.checked)}
                  disabled={disabled}
                  className="sr-only peer"
                />
                <div className="w-11 h-6 bg-gray-600 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full rtl:peer-checked:after:-translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:start-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-[#0ECB81]"></div>
              </label>
            </div>
          </div>
          {(config.enable_profit_reduce ?? true) && (
            <div className="p-4 rounded-lg" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
              <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>{t('profitReduceStep')}</label>
              <p className="text-xs mb-2" style={{ color: '#848E9C' }}>{t('profitReduceStepDesc')}</p>
              <input
                type="number"
                value={config.profit_reduce_step_pct ?? 10}
                onChange={(e) => updateField('profit_reduce_step_pct', parseFloat(e.target.value) || 10)}
                disabled={disabled}
                min={5}
                max={30}
                step={5}
                className="w-full px-3 py-2 rounded text-sm"
                style={{ background: '#2B3139', border: '1px solid #474D57', color: '#EAECEF' }}
              />
            </div>
          )}
        </div>

        <div style={{ borderTop: '1px solid #2B3139', marginBottom: '16px' }} />

        {/* Trapped Reduce */}
        <div>
          <p className="text-xs mb-3 px-1" style={{ color: '#848E9C' }}>{t('trappedReduceExplain')}</p>
          <div className="p-4 rounded-lg mb-3" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
            <div className="flex items-center justify-between">
              <div>
                <label className="block text-sm" style={{ color: '#EAECEF' }}>{t('enableTrappedReduce')}</label>
                <p className="text-xs" style={{ color: '#848E9C' }}>{t('enableTrappedReduceDesc')}</p>
              </div>
              <label className="relative inline-flex items-center cursor-pointer">
                <input
                  type="checkbox"
                  checked={config.enable_trapped_reduce ?? false}
                  onChange={(e) => updateField('enable_trapped_reduce', e.target.checked)}
                  disabled={disabled}
                  className="sr-only peer"
                />
                <div className="w-11 h-6 bg-gray-600 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full rtl:peer-checked:after:-translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:start-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-[#F6465D]"></div>
              </label>
            </div>
          </div>
          {config.enable_trapped_reduce && (
            <div className="space-y-3">
              <div className="p-4 rounded-lg" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
                <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>{t('tTradePositionThreshold')}</label>
                <p className="text-xs mb-2" style={{ color: '#848E9C' }}>{t('tTradePositionThresholdDesc')}</p>
                <input
                  type="number"
                  value={config.t_trade_position_threshold_pct ?? 30}
                  onChange={(e) => updateField('t_trade_position_threshold_pct', parseFloat(e.target.value))}
                  disabled={disabled}
                  min={10}
                  max={80}
                  step={5}
                  className="w-full px-3 py-2 rounded text-sm"
                  style={{ background: '#2B3139', border: '1px solid #474D57', color: '#EAECEF' }}
                />
              </div>
            </div>
          )}

          {/* T-Trade Spread */}
          <div className="p-4 rounded-lg mt-3" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>{t('tTradeSpread')}</label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>{t('tTradeSpreadDesc')}</p>
            <input
              type="number"
              value={config.t_trade_spread_pct ?? 0.2}
              onChange={(e) => updateField('t_trade_spread_pct', parseFloat(e.target.value) || 0.2)}
              disabled={disabled}
              min={0.2}
              max={1}
              step={0.1}
              className="w-full px-3 py-2 rounded text-sm"
              style={{ background: '#2B3139', border: '1px solid #474D57', color: '#EAECEF' }}
            />
          </div>
        </div>
      </div>

      {/* ===== Hedge Lock Section ===== */}
      <div className="p-4 rounded-lg" style={{ background: '#1A1D23', border: '1px solid #2B3139' }}>
        <div className="flex items-center gap-2 mb-4">
          <Shield className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('hedgeLockSection')}
          </h3>
        </div>
        <div className="p-4 rounded-lg" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
          <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>{t('hedgeLockThreshold')}</label>
          <p className="text-xs mb-2" style={{ color: '#848E9C' }}>{t('hedgeLockThresholdDesc')}</p>
          <input
            type="number"
            value={config.hedge_lock_threshold_pct ?? 0}
            onChange={(e) => updateField('hedge_lock_threshold_pct', parseFloat(e.target.value) || 0)}
            disabled={disabled}
            min={0}
            max={100}
            step={5}
            className="w-full px-3 py-2 rounded text-sm"
            style={{ background: '#2B3139', border: '1px solid #474D57', color: '#EAECEF' }}
          />
        </div>
      </div>
    </div>
  )
}
