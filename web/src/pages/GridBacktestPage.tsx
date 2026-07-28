import { useState, useRef, useEffect } from 'react'
import { useAuth } from '../contexts/AuthContext'
import { useLanguage } from '../contexts/LanguageContext'
import { FlaskConical, Loader2, Play, Square, AlertTriangle, CheckCircle2 } from 'lucide-react'
import { notify } from '../lib/notify'
import { DeepVoidBackground } from '../components/DeepVoidBackground'

const API_BASE = import.meta.env.VITE_API_BASE || ''

interface GridParams {
  grid_count: number
  atr_multiplier: number
  distribution: string
  leverage: number
  profit_reduce_step_pct: number
  profit_reduce_multiplier: number
}

interface SimResult {
  final_equity: number
  return_pct: number
  max_drawdown_pct: number
  filled_levels: number
  long_reduces: number
  short_reduces: number
  blew_up: boolean
  score: number
}

export function GridBacktestPage() {
  const { token } = useAuth()
  const { language } = useLanguage()

  const [symbol, setSymbol] = useState('HYPEUSDT')
  const [timeframe, setTimeframe] = useState('15m')
  const [days, setDays] = useState(60)
  const [investment, setInvestment] = useState(1000)
  const [leverage, setLeverage] = useState(5)
  const [iterations, setIterations] = useState(2000)

  // Baseline grid params — prefilled from the active strategy's grid_config
  // when available (see useEffect below), otherwise a generic guess.
  const [gridCount, setGridCount] = useState(20)
  const [atrMultiplier, setAtrMultiplier] = useState(3.0)
  const [distribution, setDistribution] = useState('gaussian')
  const [profitReduceStepPct, setProfitReduceStepPct] = useState(6)
  const [profitReduceMultiplier, setProfitReduceMultiplier] = useState(0.1)
  const [loadedFromActiveStrategy, setLoadedFromActiveStrategy] = useState(false)

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
      returnPct: { zh: '收益率', en: 'Return' },
      maxDrawdown: { zh: '最大回撤', en: 'Max drawdown' },
      filledLevels: { zh: '成交层数', en: 'Filled levels' },
      reduces: { zh: '减仓次数(多/空)', en: 'Reduces (long/short)' },
      score: { zh: '评分', en: 'Score' },
      blewUp: { zh: '⚠️ 该组合触发全仓强平（按简化维持保证金率估算），风险极高', en: '⚠️ This combination triggered cross-margin liquidation (approximated maintenance margin rate) — very high risk' },
      clickToRun: { zh: '设置参数后点击"开始回测"', en: 'Set parameters and click "Run backtest"' },
      loadedFromActive: { zh: '已从当前激活策略加载基准参数', en: 'Baseline params loaded from active strategy' },
      fillModelNote: {
        zh: '成交模型简化：K线最高/最低价覆盖某层价格即视为成交，不模拟部分成交、做市排队和手续费。爆仓判断按全仓模式，用固定维持保证金率（0.5%）估算，不是交易所精确的分层保证金率表。结果仅供参考。',
        en: 'Simplified fill model: a level fills once a bar\'s high/low range crosses its price — no partial fills, maker queue, or fees modeled. Liquidation is approximated for cross-margin using a flat 0.5% maintenance margin rate, not an exchange\'s exact tiered schedule. Results are indicative only.',
      },
    }
    return translations[key]?.[language] || key
  }

  // Prefill baseline params from the currently active strategy's grid_config,
  // if one exists and is a grid strategy. Falls back to the generic guess
  // (already set as initial state) if there's no active strategy, it's not
  // a grid strategy, or the request fails — this is a convenience prefill,
  // not a hard requirement.
  useEffect(() => {
    if (!token) return
    ;(async () => {
      try {
        const resp = await fetch(`${API_BASE}/api/strategies/active`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!resp.ok) return
        const data = await resp.json()
        const gc = data?.config?.grid_config
        if (!gc) return
        if (gc.symbol) setSymbol(gc.symbol)
        if (typeof gc.leverage === 'number') setLeverage(gc.leverage)
        if (typeof gc.grid_count === 'number') setGridCount(gc.grid_count)
        if (typeof gc.atr_multiplier === 'number') setAtrMultiplier(gc.atr_multiplier)
        if (gc.distribution) setDistribution(gc.distribution)
        if (typeof gc.profit_reduce_step_pct === 'number') setProfitReduceStepPct(gc.profit_reduce_step_pct)
        if (typeof gc.profit_reduce_multiplier === 'number') setProfitReduceMultiplier(gc.profit_reduce_multiplier)
        if (typeof gc.total_investment === 'number' && gc.total_investment > 0) setInvestment(gc.total_investment)
        setLoadedFromActiveStrategy(true)
      } catch {
        // no active strategy / not a grid strategy / request failed — keep generic defaults
      }
    })()
  }, [token])

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

          {loadedFromActiveStrategy && (
            <div className="flex items-center gap-2 text-xs text-green-400 mb-4">
              <CheckCircle2 className="w-3.5 h-3.5" />
              {t('loadedFromActive')}
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
          </div>

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
