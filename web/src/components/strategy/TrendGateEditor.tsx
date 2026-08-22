import type { TrendGateConfig } from '../../types'

type Props = {
  config?: TrendGateConfig
  onChange: (config: TrendGateConfig) => void
  disabled?: boolean
  language: 'zh' | 'en'
}

export function TrendGateEditor({ config, onChange, disabled = false, language }: Props) {
  const value: TrendGateConfig = {
    enabled: false,
    timeframe: '5m',
    lookback: 20,
    min_price_change_pct: 1,
    min_volume_ratio: 1.2,
    ...config,
  }
  const update = (patch: Partial<TrendGateConfig>) => onChange({ ...value, ...patch })
  const zh = language === 'zh'

  return (
    <div className="space-y-3 text-sm">
      <label className="flex items-center justify-between gap-3">
        <span className="text-nofx-text">{zh ? '启用 K 线+成交量开仓门控' : 'Enable candle and volume entry gate'}</span>
        <input type="checkbox" checked={value.enabled} disabled={disabled} onChange={(e) => update({ enabled: e.target.checked })} />
      </label>
      <p className="text-xs text-nofx-text-muted">
        {zh ? '仅限制新开多/空仓，平仓、止损和风险退出始终允许。数据不足时拒绝新开仓。' : 'Only blocks new long/short entries. Closing and risk exits always remain available.'}
      </p>
      <div className="grid grid-cols-2 gap-3">
        <label className="text-xs text-nofx-text-muted">
          {zh ? '周期' : 'Timeframe'}
          <select className="mt-1 w-full rounded border border-nofx-border bg-nofx-bg px-2 py-1.5 text-nofx-text" value={value.timeframe} disabled={disabled} onChange={(e) => update({ timeframe: e.target.value })}>
            {['3m', '5m', '15m', '1h', '4h'].map((tf) => <option key={tf}>{tf}</option>)}
          </select>
        </label>
        <label className="text-xs text-nofx-text-muted">
          {zh ? '回看根数' : 'Lookback candles'}
          <input className="mt-1 w-full rounded border border-nofx-border bg-nofx-bg px-2 py-1.5 text-nofx-text" type="number" min={2} max={200} value={value.lookback} disabled={disabled} onChange={(e) => update({ lookback: Number(e.target.value) })} />
        </label>
        <label className="text-xs text-nofx-text-muted">
          {zh ? '最低价格变化 %' : 'Min price change %'}
          <input className="mt-1 w-full rounded border border-nofx-border bg-nofx-bg px-2 py-1.5 text-nofx-text" type="number" step="0.1" min={0} value={value.min_price_change_pct} disabled={disabled} onChange={(e) => update({ min_price_change_pct: Number(e.target.value) })} />
        </label>
        <label className="text-xs text-nofx-text-muted">
          {zh ? '最低成交量比' : 'Min volume ratio'}
          <input className="mt-1 w-full rounded border border-nofx-border bg-nofx-bg px-2 py-1.5 text-nofx-text" type="number" step="0.1" min={0} value={value.min_volume_ratio} disabled={disabled} onChange={(e) => update({ min_volume_ratio: Number(e.target.value) })} />
        </label>
      </div>
    </div>
  )
}
