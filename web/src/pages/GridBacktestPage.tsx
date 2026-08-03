import { useState, useRef, useEffect } from 'react'
import { useAuth } from '../contexts/AuthContext'
import { useLanguage } from '../contexts/LanguageContext'
import { FlaskConical, Loader2, Play, Square, AlertTriangle, CheckCircle2 } from 'lucide-react'
import { notify } from '../lib/notify'
import { DeepVoidBackground } from '../components/DeepVoidBackground'
import type { Strategy } from '../types'

const API_BASE = import.meta.env.VITE_API_BASE || ''

interface GridParams {
  grid_count: number
  atr_multiplier: number
  distribution: string
  leverage: number
  profit_reduce_step_pct: number
  profit_reduce_multiplier: number
  enable_trapped_reduce: boolean
  t_trade_position_threshold_pct: number
  t_trade_spread_pct: number
  profit_drawdown_threshold: number
  enable_small_position_close: boolean
  fee_pct: number
  score_mode: string
}

interface SimResult {
  final_equity: number
  return_pct: number
  max_drawdown_pct: number
  filled_levels: number
  long_reduces: number
  short_reduces: number
  t_trade_reduces: number
  drawdown_closes: number
  small_position_closes: number
  total_fees_paid: number
  blew_up: boolean
  score: number
}

export function GridBacktestPage() {
  const { token } = useAuth()
  const { language } = useLanguage()

  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [selectedStrategyId, setSelectedStrategyId] = useState('')

  const [symbol, setSymbol] = useState('HYPEUSDT')
  const [timeframe, setTimeframe] = useState('15m')
  const [days, setDays] = useState(60)
  const [investment, setInvestment] = useState(1000)
  const [leverage, setLeverage] = useState(5)
  const [iterations, setIterations] = useState(2000)

  // Baseline grid params — prefilled from the selected strategy's
  // grid_config (see applyStrategyConfig below), otherwise a generic guess.
  const [gridCount, setGridCount] = useState(20)
  const [atrMultiplier, setAtrMultiplier] = useState(3.0)
  const [distribution, setDistribution] = useState('gaussian')
  const [profitReduceStepPct, setProfitReduceStepPct] = useState(6)
  const [profitReduceMultiplier, setProfitReduceMultiplier] = useState(0.1)
  const [enableTTrade, setEnableTTrade] = useState(false)
  const [ttradePositionThresholdPct, setTtradePositionThresholdPct] = useState(30)
  const [ttradeSpreadPct, setTtradeSpreadPct] = useState(0.2)
  const [profitDrawdownThresholdPct, setProfitDrawdownThresholdPct] = useState(0)
  const [enableSmallPositionClose, setEnableSmallPositionClose] = useState(false)
  const [feePct, setFeePct] = useState(0.02)
  const [scoreMode, setScoreMode] = useState('balanced')
  const [loadedFromStrategy, setLoadedFromStrategy] = useState(false)

  const [isRunning, setIsRunning] = useState(false)
  const [baseline, setBaseline] = useState<{ params: GridParams; result: SimResult } | null>(null)
  const [best, setBest] = useState<{ params: GridParams; result: SimResult } | null>(null)
  const [progress, setProgress] = useState<{ iteration: number; iterations: number; bestScore: number } | null>(null)
  const [errorMsg, setErrorMsg] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const t = (key: string) => {
    const translations: Record<string, { zh: string; en: string }> = {
      title: { zh: '网格策略回测', en: 'Grid Strategy Backtest' },
      subtitle: {
        zh: '离线回放历史K线，用模拟退火搜索较优的网格参数组合。仅供参考，不会写回任何策略配置。',
        en: 'Offline replay against historical klines; simulated annealing searches for a better-scoring parameter set. Suggestion only — nothing is written back to any strategy config.',
      },
      selectStrategy: { zh: '选择策略（用于加载基准参数）', en: 'Select strategy (loads baseline params)' },
      noStrategies: { zh: '暂无策略', en: 'No strategies' },
      customBaseline: { zh: '（自定义基准参数）', en: '(custom baseline params)' },
      symbol: { zh: '交易对', en: 'Symbol' },
      timeframe: { zh: 'K线周期', en: 'Timeframe' },
      days: { zh: '回测天数', en: 'Backtest days' },
      investment: { zh: '起始投入 (USDT)', en: 'Starting investment (USDT)' },
      leverage: { zh: '起始杠杆', en: 'Starting leverage' },
      iterations: { zh: '退火迭代次数', en: 'Annealing iterations' },
      run: { zh: '开始回测', en: 'Run backtest' },
      stop: { zh: '停止', en: 'Stop' },
      running: { zh: '运行中...', en: 'Running...' },
      baselineTitle: { zh: '基准参数（初始猜测）', en: 'Baseline (initial guess)' },
      bestTitle: { zh: '搜索到的最优参数', en: 'Best found' },
      progressLabel: { zh: '搜索进度', en: 'Search progress' },
      gridCount: { zh: '网格数', en: 'Grid count' },
      atrMultiplier: { zh: 'ATR 倍数', en: 'ATR multiplier' },
      distribution: { zh: '分布', en: 'Distribution' },
      profitReduceStep: { zh: '止盈减仓步进 %', en: 'Profit-reduce step %' },
      profitReduceMultiplier: { zh: '止盈减仓倍率', en: 'Profit-reduce multiplier' },
      enableTTrade: { zh: '启用 T 字被套减仓', en: 'Enable T-trade (trapped reduce)' },
      ttradePositionThreshold: { zh: 'T字触发仓位 (%)', en: 'T-trade trigger position (%)' },
      ttradeSpread: { zh: 'T字减仓价差 (%)', en: 'T-trade reduce spread (%)' },
      profitDrawdownThreshold: { zh: '利润回撤阈值 (%)', en: 'Profit drawdown threshold (%)' },
      profitDrawdownHint: { zh: '0 = 禁用', en: '0 = disabled' },
      enableSmallPositionClose: { zh: '小仓位自动平仓', en: 'Auto-close small positions' },
      feePct: { zh: '手续费率 % (0=禁用)', en: 'Fee rate % (0 disables)' },
      scoreMode: { zh: '评分策略', en: 'Score Mode' },
      scoreModeDesc: {
        zh: '控制退火搜索在"收益"与"回撤"之间怎么取舍，不影响单次回测的成交/手续费/回撤本身，只影响搜索器最终偏好哪组参数。',
        en: 'Controls the trade-off the annealing search makes between return and drawdown. Doesn\'t change a single run\'s fills/fees/drawdown — only which parameter set the search ends up favoring.',
      },
      scoreModeBalanced: { zh: '收益与风险均衡（推荐）', en: 'Balanced (Return & Risk, recommended)' },
      scoreModeReturnFocused: { zh: '收益优先', en: 'Return-focused' },
      returnPct: { zh: '收益率', en: 'Return' },
      maxDrawdown: { zh: '最大回撤', en: 'Max drawdown' },
      filledLevels: { zh: '成交层数', en: 'Filled levels' },
      reduces: { zh: '减仓次数(多/空)', en: 'Reduces (long/short)' },
      ttradeReduces: { zh: 'T字减仓次数', en: 'T-trade reduces' },
      drawdownCloses: { zh: '回撤全平次数', en: 'Drawdown closes' },
      smallCloses: { zh: '小仓位平仓次数', en: 'Small-position closes' },
      totalFeesPaid: { zh: '累计手续费', en: 'Total fees paid' },
      score: { zh: '评分', en: 'Score' },
      blewUp: { zh: '⚠️ 该组合触发全仓强平（按简化维持保证金率估算），风险极高', en: '⚠️ This combination triggered cross-margin liquidation (approximated maintenance margin rate) — very high risk' },
      clickToRun: { zh: '设置参数后点击"开始回测"', en: 'Set parameters and click "Run backtest"' },
      loadedFromStrategy: { zh: '已从所选策略加载基准参数', en: 'Baseline params loaded from selected strategy' },
      fillModelNote: {
        zh: '成交模型简化：K线最高/最低价覆盖某层价格即视为成交，不模拟部分成交和做市排队。手续费按固定费率模拟（对每笔成交的名义价值收取）。爆仓判断按全仓模式，用固定维持保证金率（0.5%）估算，不是交易所精确的分层保证金率表。T字被套减仓、利润回撤全平、小仓位自动平仓均已按对应实盘逻辑复刻，但仍是简化模型。结果仅供参考。',
        en: 'Simplified fill model: a level fills once a bar\'s high/low range crosses its price — no partial fills or maker queue modeled. Trading fees are simulated as a flat rate on each fill\'s notional. Liquidation is approximated for cross-margin using a flat 0.5% maintenance margin rate, not an exchange\'s exact tiered schedule. T-trade, profit-drawdown close, and small-position close are ported from the corresponding live logic, but remain simplified models. Results are indicative only.',
      },
    }
    return translations[key]?.[language] || key
  }

  // Fetch the user's strategy list once. Reused by PromptTestPage's pattern
  // (GET /api/strategies), not the single active strategy.
  useEffect(() => {
    if (!token) return
    ;(async () => {
      try {
        const resp = await fetch(`${API_BASE}/api/strategies`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!resp.ok) return
        const data = await resp.json()
        setStrategies(data.strategies || [])
      } catch {
        // no strategies / request failed — the form still works with manual params
      }
    })()
  }, [token])

  // Prefill baseline params from the selected strategy's grid_config, if it
  // has one. Selecting "" (no strategy) leaves whatever is currently in the
  // form untouched — this is a convenience prefill, not a hard requirement.
  const applyStrategyConfig = (strategyId: string) => {
    setSelectedStrategyId(strategyId)
    setLoadedFromStrategy(false)
    if (!strategyId) return
    const strategy = strategies.find((s) => s.id === strategyId)
    const gc = strategy?.config?.grid_config
    if (!gc) return
    if (gc.symbol) setSymbol(gc.symbol)
    if (typeof gc.leverage === 'number') setLeverage(gc.leverage)
    if (typeof gc.grid_count === 'number') setGridCount(gc.grid_count)
    if (typeof gc.atr_multiplier === 'number') setAtrMultiplier(gc.atr_multiplier)
    if (gc.distribution) setDistribution(gc.distribution)
    if (typeof gc.profit_reduce_step_pct === 'number') setProfitReduceStepPct(gc.profit_reduce_step_pct)
    if (typeof gc.profit_reduce_multiplier === 'number') setProfitReduceMultiplier(gc.profit_reduce_multiplier)
    if (typeof gc.enable_trapped_reduce === 'boolean') setEnableTTrade(gc.enable_trapped_reduce)
    if (typeof gc.t_trade_position_threshold_pct === 'number') setTtradePositionThresholdPct(gc.t_trade_position_threshold_pct)
    if (typeof gc.t_trade_spread_pct === 'number') setTtradeSpreadPct(gc.t_trade_spread_pct)
    if (typeof gc.profit_drawdown_threshold === 'number') setProfitDrawdownThresholdPct(gc.profit_drawdown_threshold)
    if (typeof gc.enable_small_position_close === 'boolean') setEnableSmallPositionClose(gc.enable_small_position_close)
    if (typeof gc.total_investment === 'number' && gc.total_investment > 0) setInvestment(gc.total_investment)
    setLoadedFromStrategy(true)
  }

  const stop = () => {
    abortRef.current?.abort()
    setIsRunning(false)
  }

  const run = async () => {
    if (!token) return
    setIsRunning(true)
    setBaseline(null)
    setBest(null)
    setProgress(null)
    setErrorMsg(null)

    const ctrl = new AbortController()
    abortRef.current = ctrl

    const params = new URLSearchParams({
      symbol,
      timeframe,
      days: String(days),
      investment: String(investment),
      leverage: String(leverage),
      iterations: String(iterations),
      grid_count: String(gridCount),
      atr_multiplier: String(atrMultiplier),
      distribution,
      profit_reduce_step_pct: String(profitReduceStepPct),
      profit_reduce_multiplier: String(profitReduceMultiplier),
      enable_trapped_reduce: String(enableTTrade),
      t_trade_position_threshold_pct: String(ttradePositionThresholdPct),
      t_trade_spread_pct: String(ttradeSpreadPct),
      profit_drawdown_threshold: String(profitDrawdownThresholdPct),
      enable_small_position_close: String(enableSmallPositionClose),
      fee_pct: String(feePct),
      score_mode: scoreMode,
    })

    try {
      const resp = await fetch(`${API_BASE}/api/backtest/grid/run?${params.toString()}`, {
        headers: { Authorization: `Bearer ${token}` },
        signal: ctrl.signal,
      })
      if (!resp.ok || !resp.body) {
        throw new Error(`Request failed (${resp.status})`)
      }
      const reader = resp.body.getReader()
      const dec = new TextDecoder()
      let buf = ''

      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        const chunks = buf.split('\n\n')
        buf = chunks.pop() ?? ''

        for (const chunk of chunks) {
          const lines = chunk.split('\n')
          let event = ''
          let data = ''
          for (const line of lines) {
            if (line.startsWith('event:')) event = line.slice(6).trim()
            else if (line.startsWith('data:')) data += line.slice(5).trim()
          }
          if (!event || !data) continue

          try {
            const parsed = JSON.parse(data)
            if (event === 'baseline') {
              setBaseline(parsed)
            } else if (event === 'progress') {
              setProgress({ iteration: parsed.iteration, iterations: parsed.iterations, bestScore: parsed.best_score })
            } else if (event === 'done') {
              setBest(parsed)
            } else if (event === 'error') {
              setErrorMsg(parsed.error || 'Unknown error')
            }
          } catch {
            // ignore malformed chunk
          }
        }
      }
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) {
        notify.error(err instanceof Error ? err.message : 'Unknown error')
      }
    } finally {
      setIsRunning(false)
    }
  }

  const renderParams = (p: GridParams) => (
    <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 text-sm">
      <div><span className="text-nofx-text-secondary">{t('gridCount')}: </span>{p.grid_count}</div>
      <div><span className="text-nofx-text-secondary">{t('atrMultiplier')}: </span>{p.atr_multiplier.toFixed(2)}</div>
      <div><span className="text-nofx-text-secondary">{t('distribution')}: </span>{p.distribution}</div>
      <div><span className="text-nofx-text-secondary">{t('leverage')}: </span>{p.leverage}x</div>
      <div><span className="text-nofx-text-secondary">{t('profitReduceStep')}: </span>{p.profit_reduce_step_pct.toFixed(1)}</div>
      <div><span className="text-nofx-text-secondary">{t('profitReduceMultiplier')}: </span>{p.profit_reduce_multiplier.toFixed(2)}</div>
      {p.enable_trapped_reduce && (
        <>
          <div><span className="text-nofx-text-secondary">{t('ttradePositionThreshold')}: </span>{p.t_trade_position_threshold_pct.toFixed(1)}</div>
          <div><span className="text-nofx-text-secondary">{t('ttradeSpread')}: </span>{p.t_trade_spread_pct.toFixed(2)}</div>
        </>
      )}
      {p.profit_drawdown_threshold > 0 && (
        <div><span className="text-nofx-text-secondary">{t('profitDrawdownThreshold')}: </span>{p.profit_drawdown_threshold.toFixed(1)}</div>
      )}
      {p.enable_small_position_close && (
        <div className="text-nofx-text-secondary">{t('enableSmallPositionClose')}: ✓</div>
      )}
      {p.fee_pct > 0 && (
        <div><span className="text-nofx-text-secondary">{t('feePct')}: </span>{p.fee_pct.toFixed(3)}%</div>
      )}
      {p.score_mode === 'return_focused' && (
        <div className="text-nofx-text-secondary">{t('scoreMode')}: {t('scoreModeReturnFocused')}</div>
      )}
    </div>
  )

  const renderResult = (r: SimResult) => (
    <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 text-sm mt-3 pt-3 border-t border-nofx-gold/10">
      <div>
        <span className="text-nofx-text-secondary">{t('returnPct')}: </span>
        <span className={r.return_pct >= 0 ? 'text-green-400' : 'text-red-400'}>{r.return_pct.toFixed(2)}%</span>
      </div>
      <div><span className="text-nofx-text-secondary">{t('maxDrawdown')}: </span><span className="text-red-400">{r.max_drawdown_pct.toFixed(2)}%</span></div>
      <div><span className="text-nofx-text-secondary">{t('filledLevels')}: </span>{r.filled_levels}</div>
      <div><span className="text-nofx-text-secondary">{t('reduces')}: </span>{r.long_reduces} / {r.short_reduces}</div>
      {r.t_trade_reduces > 0 && <div><span className="text-nofx-text-secondary">{t('ttradeReduces')}: </span>{r.t_trade_reduces}</div>}
      {r.drawdown_closes > 0 && <div><span className="text-nofx-text-secondary">{t('drawdownCloses')}: </span>{r.drawdown_closes}</div>}
      {r.small_position_closes > 0 && <div><span className="text-nofx-text-secondary">{t('smallCloses')}: </span>{r.small_position_closes}</div>}
      {r.total_fees_paid > 0 && <div><span className="text-nofx-text-secondary">{t('totalFeesPaid')}: </span>{r.total_fees_paid.toFixed(2)}</div>}
      <div><span className="text-nofx-text-secondary">{t('score')}: </span>{r.score.toFixed(2)}</div>
      {r.blew_up && (
        <div className="col-span-2 sm:col-span-3 flex items-center gap-2 text-amber-400">
          <AlertTriangle className="w-4 h-4" /> {t('blewUp')}
        </div>
      )}
    </div>
  )

  return (
    <DeepVoidBackground>
      <div className="max-w-5xl mx-auto px-4 py-8">
        <div className="mb-6">
          <h1 className="text-3xl font-bold text-nofx-text mb-2 flex items-center gap-2">
            <FlaskConical className="w-7 h-7 text-nofx-gold" />
            {t('title')}
          </h1>
          <p className="text-nofx-text-secondary">{t('subtitle')}</p>
        </div>

        <div className="bg-nofx-bg-lighter/50 backdrop-blur-sm rounded-lg border border-nofx-gold/20 p-6 mb-6">
          <div className="mb-4">
            <label className="block text-xs text-nofx-text-secondary mb-1">{t('selectStrategy')}</label>
            <select
              value={selectedStrategyId}
              onChange={(e) => applyStrategyConfig(e.target.value)}
              disabled={isRunning}
              className="w-full max-w-md px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
            >
              <option value="">{strategies.length === 0 ? t('noStrategies') : t('customBaseline')}</option>
              {strategies.map((s) => (
                <option key={s.id} value={s.id}>{s.name}</option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 mb-4">
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('symbol')}</label>
              <input
                value={symbol}
                onChange={(e) => setSymbol(e.target.value.toUpperCase())}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('timeframe')}</label>
              <select
                value={timeframe}
                onChange={(e) => setTimeframe(e.target.value)}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              >
                {['5m', '15m', '1h', '4h'].map((tf) => (
                  <option key={tf} value={tf}>{tf}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('days')}</label>
              <input
                type="number"
                min={7}
                max={365}
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('investment')}</label>
              <input
                type="number"
                min={10}
                value={investment}
                onChange={(e) => setInvestment(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('leverage')}</label>
              <input
                type="number"
                min={1}
                max={10}
                value={leverage}
                onChange={(e) => setLeverage(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('iterations')}</label>
              <input
                type="number"
                min={100}
                max={20000}
                step={100}
                value={iterations}
                onChange={(e) => setIterations(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
          </div>

          {loadedFromStrategy && (
            <div className="flex items-center gap-2 text-xs text-green-400 mb-4">
              <CheckCircle2 className="w-3.5 h-3.5" />
              {t('loadedFromStrategy')}
            </div>
          )}

          <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 mb-4">
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('gridCount')}</label>
              <input
                type="number"
                min={2}
                max={100}
                value={gridCount}
                onChange={(e) => setGridCount(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('atrMultiplier')}</label>
              <input
                type="number"
                min={0.1}
                max={20}
                step={0.1}
                value={atrMultiplier}
                onChange={(e) => setAtrMultiplier(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('distribution')}</label>
              <select
                value={distribution}
                onChange={(e) => setDistribution(e.target.value)}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              >
                {['gaussian', 'pyramid', 'uniform'].map((d) => (
                  <option key={d} value={d}>{d}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('profitReduceStep')}</label>
              <input
                type="number"
                min={0.1}
                max={100}
                step={0.5}
                value={profitReduceStepPct}
                onChange={(e) => setProfitReduceStepPct(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('profitReduceMultiplier')}</label>
              <input
                type="number"
                min={0.01}
                max={1}
                step={0.01}
                value={profitReduceMultiplier}
                onChange={(e) => setProfitReduceMultiplier(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('profitDrawdownThreshold')}</label>
              <input
                type="number"
                min={0}
                max={100}
                step={1}
                value={profitDrawdownThresholdPct}
                onChange={(e) => setProfitDrawdownThresholdPct(Number(e.target.value))}
                disabled={isRunning}
                placeholder={t('profitDrawdownHint')}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('feePct')}</label>
              <input
                type="number"
                min={0}
                max={1}
                step={0.001}
                value={feePct}
                onChange={(e) => setFeePct(Number(e.target.value))}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              />
            </div>
            <div>
              <label className="block text-xs text-nofx-text-secondary mb-1">{t('scoreMode')}</label>
              <select
                value={scoreMode}
                onChange={(e) => setScoreMode(e.target.value)}
                disabled={isRunning}
                className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
              >
                <option value="balanced">{t('scoreModeBalanced')}</option>
                <option value="return_focused">{t('scoreModeReturnFocused')}</option>
              </select>
            </div>
          </div>
          <p className="text-xs text-nofx-text-secondary/70 -mt-2 mb-4">{t('scoreModeDesc')}</p>

          <div className="flex flex-wrap items-center gap-6 mb-4">
            <label className="flex items-center gap-2 text-sm text-nofx-text cursor-pointer">
              <input
                type="checkbox"
                checked={enableTTrade}
                onChange={(e) => setEnableTTrade(e.target.checked)}
                disabled={isRunning}
                className="w-4 h-4 accent-nofx-gold"
              />
              {t('enableTTrade')}
            </label>
            <label className="flex items-center gap-2 text-sm text-nofx-text cursor-pointer">
              <input
                type="checkbox"
                checked={enableSmallPositionClose}
                onChange={(e) => setEnableSmallPositionClose(e.target.checked)}
                disabled={isRunning}
                className="w-4 h-4 accent-nofx-gold"
              />
              {t('enableSmallPositionClose')}
            </label>
          </div>

          {enableTTrade && (
            <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 mb-4">
              <div>
                <label className="block text-xs text-nofx-text-secondary mb-1">{t('ttradePositionThreshold')}</label>
                <input
                  type="number"
                  min={1}
                  max={100}
                  step={1}
                  value={ttradePositionThresholdPct}
                  onChange={(e) => setTtradePositionThresholdPct(Number(e.target.value))}
                  disabled={isRunning}
                  className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
                />
              </div>
              <div>
                <label className="block text-xs text-nofx-text-secondary mb-1">{t('ttradeSpread')}</label>
                <input
                  type="number"
                  min={0.01}
                  max={10}
                  step={0.05}
                  value={ttradeSpreadPct}
                  onChange={(e) => setTtradeSpreadPct(Number(e.target.value))}
                  disabled={isRunning}
                  className="w-full px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold disabled:opacity-50"
                />
              </div>
            </div>
          )}

          <div className="flex items-center gap-3">
            {!isRunning ? (
              <button
                onClick={run}
                className="flex items-center gap-2 px-5 py-2 rounded-lg font-medium transition-colors bg-nofx-gold hover:bg-nofx-gold/80 text-nofx-bg"
              >
                <Play className="w-4 h-4" />
                {t('run')}
              </button>
            ) : (
              <button
                onClick={stop}
                className="flex items-center gap-2 px-5 py-2 rounded-lg font-medium transition-colors bg-red-600 hover:bg-red-700 text-white"
              >
                <Square className="w-4 h-4" />
                {t('stop')}
              </button>
            )}
            {isRunning && progress && (
              <div className="flex items-center gap-2 text-sm text-nofx-text-secondary">
                <Loader2 className="w-4 h-4 animate-spin" />
                {t('progressLabel')}: {progress.iteration}/{progress.iterations} — {t('score')} {progress.bestScore.toFixed(2)}
              </div>
            )}
          </div>

          {errorMsg && (
            <div className="mt-4 flex items-center gap-2 text-red-400 text-sm">
              <AlertTriangle className="w-4 h-4" /> {errorMsg}
            </div>
          )}
        </div>

        {baseline || best ? (
          <div className="grid sm:grid-cols-2 gap-4">
            {baseline && (
              <div className="bg-nofx-bg-lighter/50 backdrop-blur-sm rounded-lg border border-nofx-gold/20 p-5">
                <h3 className="text-sm font-semibold text-nofx-text mb-3">{t('baselineTitle')}</h3>
                {renderParams(baseline.params)}
                {renderResult(baseline.result)}
              </div>
            )}
            {best && (
              <div className="bg-nofx-bg-lighter/50 backdrop-blur-sm rounded-lg border border-green-500/30 p-5">
                <h3 className="text-sm font-semibold text-green-400 mb-3">{t('bestTitle')}</h3>
                {renderParams(best.params)}
                {renderResult(best.result)}
              </div>
            )}
          </div>
        ) : (
          !isRunning && (
            <div className="flex flex-col items-center justify-center py-12 text-nofx-text-secondary">
              <FlaskConical className="w-12 h-12 mb-3 opacity-50" />
              <p>{t('clickToRun')}</p>
            </div>
          )
        )}

        <p className="mt-6 text-xs text-nofx-text-secondary/70">{t('fillModelNote')}</p>
      </div>
    </DeepVoidBackground>
  )
}
