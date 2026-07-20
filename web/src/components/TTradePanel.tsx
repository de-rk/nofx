import { useState } from 'react'
import type { GridTradeLog } from '../types'

interface TTradeGroup {
  prepOrderId: string
  symbol: string
  side: string
  events: GridTradeLog[]
  tagLog?: GridTradeLog
  fillLog?: GridTradeLog
  reducePlacedLog?: GridTradeLog
  reduceLog?: GridTradeLog
}

function groupTTradeEvents(logs: GridTradeLog[]): TTradeGroup[] {
  const ttradeLogs = logs.filter(l => l.source === 'ttrade')
  const groups = new Map<string, GridTradeLog[]>()

  for (const log of ttradeLogs) {
    if (!log.order_id) continue
    if (!groups.has(log.order_id)) {
      groups.set(log.order_id, [])
    }
    groups.get(log.order_id)!.push(log)
  }

  const result: TTradeGroup[] = []
  for (const [prepOrderId, events] of groups.entries()) {
    events.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
    const tagLog = events.find(e => e.action === 'ttrade_tag')
    const fillLog = events.find(e => e.action === 'ttrade_fill')
    const reducePlacedLog = events.find(e => e.action === 'ttrade_reduce_placed')
    const reduceLog = events.find(e => e.action === 'ttrade_reduce')

    result.push({
      prepOrderId,
      symbol: tagLog?.symbol || events[0].symbol,
      side: tagLog?.side || events[0].side,
      events,
      tagLog,
      fillLog,
      reducePlacedLog,
      reduceLog,
    })
  }

  return result.sort((a, b) => {
    const tA = new Date(a.events[0].created_at).getTime()
    const tB = new Date(b.events[0].created_at).getTime()
    return tB - tA
  })
}

const ACTION_ICONS: Record<string, string> = {
  ttrade_tag: '☑️',
  ttrade_fill: '✅',
  ttrade_reduce_placed: '📌',
  ttrade_reduce: '🔁',
}

const ACTION_LABELS: Record<string, string> = {
  ttrade_tag: '标记',
  ttrade_fill: '成交',
  ttrade_reduce_placed: '减仓挂单',
  ttrade_reduce: '减仓成交',
}

function formatTime(iso: string) {
  return new Date(iso).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function TTradePanel({ logs }: { logs?: GridTradeLog[] }) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  if (!logs || logs.length === 0) {
    return (
      <div className="py-16 text-center text-nofx-text-muted opacity-60">
        <div className="text-6xl mb-4 opacity-30">🎯</div>
        <div className="text-lg font-semibold mb-2 text-nofx-text-main">暂无 T-trade 记录</div>
        <div className="text-sm">T-trade 生命周期将在此显示</div>
      </div>
    )
  }

  const groups = groupTTradeEvents(logs)

  if (groups.length === 0) {
    return (
      <div className="py-16 text-center text-nofx-text-muted opacity-60">
        <div className="text-6xl mb-4 opacity-30">🎯</div>
        <div className="text-lg font-semibold mb-2 text-nofx-text-main">暂无 T-trade 记录</div>
        <div className="text-sm">标记单成交后将显示完整生命周期</div>
      </div>
    )
  }

  const toggle = (id: string) => {
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  return (
    <div className="space-y-3">
      {groups.map(({ prepOrderId, symbol, side, events, tagLog, fillLog, reducePlacedLog, reduceLog }) => {
        const isExpanded = expanded.has(prepOrderId)
        const isBuy = side === 'buy'
        const sideColor = isBuy ? 'text-green-400' : 'text-red-400'
        const sideBg = isBuy ? 'bg-green-500/10' : 'bg-red-500/10'

        // Status badge
        let status = '标记'
        let statusColor = 'bg-gray-500/20 text-gray-400'
        if (reduceLog) {
          status = '已完成'
          statusColor = 'bg-green-500/20 text-green-400'
        } else if (reducePlacedLog) {
          status = '等待减仓'
          statusColor = 'bg-blue-500/20 text-blue-400'
        } else if (fillLog) {
          status = '已成交'
          statusColor = 'bg-yellow-500/20 text-yellow-400'
        }

        return (
          <div key={prepOrderId} className="border border-white/5 rounded-lg overflow-hidden bg-black/20">
            {/* Summary Row */}
            <button
              onClick={() => toggle(prepOrderId)}
              className="w-full px-4 py-3 flex items-center gap-3 hover:bg-white/[0.02] transition-colors text-left"
            >
              <div className={`px-2 py-1 rounded text-xs font-medium ${sideBg} ${sideColor} shrink-0`}>
                {isBuy ? '多' : '空'}
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 mb-1">
                  <span className="text-sm font-mono text-nofx-text-main">{symbol}</span>
                  <span className={`text-xs px-2 py-0.5 rounded ${statusColor}`}>{status}</span>
                </div>
                <div className="text-xs text-nofx-text-muted truncate">
                  {tagLog && `标记 ${tagLog.price?.toFixed(4)} × ${tagLog.quantity?.toFixed(2)}`}
                  {fillLog && ` → 成交 ${fillLog.price?.toFixed(4)}`}
                  {reduceLog && ` → 减仓 ${reduceLog.price?.toFixed(4)}`}
                </div>
              </div>
              <div className="text-xs text-nofx-text-muted shrink-0">
                {tagLog && formatTime(tagLog.created_at)}
              </div>
              <div className={`transition-transform ${isExpanded ? 'rotate-180' : ''}`}>
                <svg className="w-4 h-4 text-nofx-text-muted" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
                </svg>
              </div>
            </button>

            {/* Expanded Details */}
            {isExpanded && (
              <div className="border-t border-white/5 bg-black/20">
                {events.map((event, idx) => (
                  <div
                    key={idx}
                    className="px-4 py-2 border-b border-white/5 last:border-b-0 hover:bg-white/[0.01] transition-colors"
                  >
                    <div className="flex items-start gap-3">
                      <div className="text-lg shrink-0">{ACTION_ICONS[event.action] || '•'}</div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 mb-1">
                          <span className="text-xs font-medium text-nofx-text-main">{ACTION_LABELS[event.action] || event.action}</span>
                          {event.price && event.price > 0 && (
                            <span className="text-xs text-nofx-accent font-mono">${event.price.toFixed(4)}</span>
                          )}
                          {event.quantity && event.quantity > 0 && (
                            <span className="text-xs text-nofx-text-muted">×{event.quantity.toFixed(2)}</span>
                          )}
                        </div>
                        {event.reason && (
                          <div className="text-xs text-nofx-text-muted truncate">{event.reason}</div>
                        )}
                      </div>
                      <div className="text-xs text-nofx-text-muted shrink-0 tabular-nums">
                        {formatTime(event.created_at)}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
