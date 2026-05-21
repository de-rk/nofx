import { useEffect, useRef, useState } from 'react'
import {
  createChart,
  IChartApi,
  ISeriesApi,
  Time,
  UTCTimestamp,
  CandlestickSeries,
  HistogramSeries,
  createSeriesMarkers,
} from 'lightweight-charts'
import { useLanguage } from '../contexts/LanguageContext'
import { httpClient } from '../lib/httpClient'
import { type Kline } from '../utils/indicators'

// 订单接口定义
interface OrderMarker {
  time: number
  price: number
  side: 'long' | 'short'
  rawSide: string // 原始 side 字段 (buy/sell from database)
  action: 'open' | 'close'
  pnl?: number
  symbol: string
}

// 挂单接口定义 (交易所的止盈止损订单)
interface OpenOrder {
  order_id: string
  symbol: string
  side: string          // BUY/SELL
  position_side: string // LONG/SHORT
  type: string          // LIMIT/STOP_MARKET/TAKE_PROFIT_MARKET
  price: number         // 限价单价格
  stop_price: number    // 触发价格 (止损/止盈)
  quantity: number
  status: string
}

interface AdvancedChartProps {
  symbol: string
  interval?: string
  traderID?: string
  height?: number
  exchange?: string // 交易所类型：binance, bybit, okx, bitget, hyperliquid, aster, lighter
  onSymbolChange?: (symbol: string) => void // 币种切换回调
}

// 获取成交额货币单位
const getQuoteUnit = (exchange: string): string => {
  if (['alpaca'].includes(exchange)) {
    return 'USD'
  }
  if (['forex', 'metals'].includes(exchange)) {
    return '' // 外汇/贵金属没有真实成交量
  }
  return 'USDT' // 加密货币默认 USDT
}

// 获取成交量数量单位
const getBaseUnit = (exchange: string, symbol: string): string => {
  if (['alpaca'].includes(exchange)) {
    return '股'
  }
  if (['forex', 'metals'].includes(exchange)) {
    return ''
  }
  // 加密货币：从 symbol 提取基础资产
  const base = symbol.replace(/USDT$|USD$|BUSD$/, '')
  return base || '个'
}

// 格式化大数字
const formatVolume = (value: number): string => {
  if (value >= 1e9) return (value / 1e9).toFixed(2) + 'B'
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 1e3) return (value / 1e3).toFixed(2) + 'K'
  return value.toFixed(2)
}

export function AdvancedChart({
  symbol = 'BTCUSDT',
  interval = '5m',
  traderID,
  height = 550,
  exchange = 'binance', // 默认使用 binance
  onSymbolChange: _onSymbolChange, // Available for future use
}: AdvancedChartProps) {
  void _onSymbolChange // Prevent unused warning
  const { language } = useLanguage()
  const quoteUnit = getQuoteUnit(exchange)
  const baseUnit = getBaseUnit(exchange, symbol)
  const chartContainerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candlestickSeriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volumeSeriesRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const seriesMarkersRef = useRef<any>(null) // Markers primitive for v5
  const currentMarkersDataRef = useRef<any[]>([]) // 存储当前的标记数据
  const klineDataRef = useRef<Map<number, { volume: number; quoteVolume: number }>>(new Map()) // 存储 kline 额外数据
  const lastCandleTimeRef = useRef<number>(0) // 跟踪最后一根K线时间，防止 update 报错
  const priceLinesRef = useRef<any[]>([]) // 存储挂单价格线

  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showOrderMarkers, setShowOrderMarkers] = useState(true)
  const isInitialLoadRef = useRef(true) // 跟踪是否为初始加载
  const [tooltipData, setTooltipData] = useState<any>(null)
  const tooltipRef = useRef<HTMLDivElement>(null)

  // 行情统计数据（当前K线）
  const [marketStats, setMarketStats] = useState<{
    price: number
    priceChange: number
    priceChangePercent: number
    high: number
    low: number
    volume: number      // 数量（BTC/股数）
    quoteVolume: number // 成交额（USDT/USD）
  } | null>(null)

  // 从服务获取K线数据
  const fetchKlineData = async (symbol: string, interval: string, isRefresh = false) => {
    try {
      const limit = isRefresh ? 10 : 1500
      const klineUrl = `/api/klines?symbol=${symbol}&interval=${interval}&limit=${limit}&exchange=${exchange}`
      const result = await httpClient.get(klineUrl)

      if (!result.success || !result.data) {
        throw new Error('Failed to fetch kline data')
      }

      // 转换数据格式
      const rawData = result.data.map((candle: any) => ({
        time: Math.floor(candle.openTime / 1000) as UTCTimestamp,
        open: candle.open,
        high: candle.high,
        low: candle.low,
        close: candle.close,
        volume: candle.volume,           // 数量（BTC/股数）
        quoteVolume: candle.quoteVolume, // 成交额（USDT/USD）
      }))

      // 按时间排序并去重（lightweight-charts 要求数据按时间升序且无重复）
      const sortedData = rawData.sort((a: any, b: any) => a.time - b.time)
      const dedupedData = sortedData.filter((item: any, index: number, arr: any[]) =>
        index === 0 || item.time !== arr[index - 1].time
      )

      if (rawData.length !== dedupedData.length) {
        console.warn('[AdvancedChart] Removed', rawData.length - dedupedData.length, 'duplicate klines')
      }

      return dedupedData
    } catch (err) {
      console.error('[AdvancedChart] Error fetching kline:', err)
      throw err
    }
  }

  // 解析时间：支持 Unix 时间戳（数字）或字符串格式
  const parseCustomTime = (time: any): number => {
    if (!time) {
      console.warn('[AdvancedChart] Empty time value')
      return 0
    }

    // 如果已经是数字（Unix 时间戳）
    if (typeof time === 'number') {
      // 判断是毫秒还是秒：如果大于 10^12 则认为是毫秒（2001年之后的毫秒时间戳）
      if (time > 1000000000000) {
        return Math.floor(time / 1000)
      }
      return time
    }

    const timeStr = String(time)

    // 尝试标准ISO格式
    const isoTime = new Date(timeStr).getTime()
    if (!isNaN(isoTime) && isoTime > 0) {
      return Math.floor(isoTime / 1000)
    }

    // 解析自定义格式 "MM-DD HH:mm UTC" (兼容旧数据)
    const match = timeStr.match(/(\d{2})-(\d{2})\s+(\d{2}):(\d{2})\s+UTC/)
    if (match) {
      const currentYear = new Date().getFullYear()
      const [_, month, day, hour, minute] = match
      const date = new Date(Date.UTC(
        currentYear,
        parseInt(month) - 1,
        parseInt(day),
        parseInt(hour),
        parseInt(minute)
      ))
      return Math.floor(date.getTime() / 1000)
    }

    console.error('[AdvancedChart] ❌ Failed to parse time:', timeStr)
    return 0
  }

  // 获取订单数据
  const fetchOrders = async (traderID: string, symbol: string): Promise<OrderMarker[]> => {
    try {
      // 获取已成交的订单，增加到200条以显示更多历史订单
      const result = await httpClient.get(`/api/orders?trader_id=${traderID}&symbol=${symbol}&status=FILLED&limit=200`)

      if (!result.success || !result.data) {
        return []
      }

      const orders = result.data
      const markers: OrderMarker[] = []

      orders.forEach((order: any) => {

        // 处理字段名：支持PascalCase和snake_case
        const filledAt = order.filled_at || order.FilledAt || order.created_at || order.CreatedAt
        const avgPrice = order.avg_fill_price || order.AvgFillPrice || order.price || order.Price
        const orderAction = order.order_action || order.OrderAction
        const side = (order.side || order.Side)?.toLowerCase() // BUY/SELL
        const symbol = order.symbol || order.Symbol

        // 跳过没有成交时间或价格的订单
        if (!filledAt || !avgPrice || avgPrice === 0) {
          console.warn('[AdvancedChart] Skipping order - missing data:', { filledAt, avgPrice })
          return
        }

        const timeSeconds = parseCustomTime(filledAt)
        if (timeSeconds === 0) {
          console.warn('[AdvancedChart] Skipping order - invalid time:', filledAt)
          return
        }

        // 根据 order_action 判断是开仓还是平仓
        let action: 'open' | 'close' = 'open'
        let positionSide: 'long' | 'short' = 'long'

        if (orderAction) {
          if (orderAction.includes('OPEN')) {
            action = 'open'
            positionSide = orderAction.includes('LONG') ? 'long' : 'short'
          } else if (orderAction.includes('CLOSE')) {
            action = 'close'
            positionSide = orderAction.includes('LONG') ? 'long' : 'short'
          }
        } else {
          // 如果没有 order_action，根据 side 判断
          positionSide = side === 'buy' ? 'long' : 'short'
        }

        markers.push({
          time: timeSeconds,
          price: avgPrice,
          side: positionSide,
          rawSide: side, // 原始 side 字段 (buy/sell)
          action: action,
          symbol,
        })
      })

      return markers
    } catch (err) {
      console.error('[AdvancedChart] Error fetching orders:', err)
      return []
    }
  }

  // 获取交易所挂单 (止盈止损订单)
  const fetchOpenOrders = async (traderID: string, symbol: string): Promise<OpenOrder[]> => {
    try {
      const result = await httpClient.get(`/api/open-orders?trader_id=${traderID}&symbol=${symbol}`)

      if (!result.success || !result.data) {
        return []
      }

      return result.data as OpenOrder[]
    } catch (err) {
      console.error('[AdvancedChart] Error fetching open orders:', err)
      return []
    }
  }

  // 初始化图表
  useEffect(() => {
    if (!chartContainerRef.current) return

    const chart = createChart(chartContainerRef.current, {
      width: chartContainerRef.current.clientWidth || 800,
      height: chartContainerRef.current.clientHeight || height,
      layout: {
        background: { color: '#0B0E11' },
        textColor: '#B7BDC6',
        fontSize: 12,
      },
      grid: {
        vertLines: {
          color: 'rgba(43, 49, 57, 0.2)',
          style: 1,
          visible: true,
        },
        horzLines: {
          color: 'rgba(43, 49, 57, 0.2)',
          style: 1,
          visible: true,
        },
      },
      crosshair: {
        mode: 1,
        vertLine: {
          color: 'rgba(240, 185, 11, 0.5)',
          width: 1,
          style: 2,
          labelBackgroundColor: '#F0B90B',
        },
        horzLine: {
          color: 'rgba(240, 185, 11, 0.5)',
          width: 1,
          style: 2,
          labelBackgroundColor: '#F0B90B',
        },
      },
      rightPriceScale: {
        borderColor: '#2B3139',
        scaleMargins: {
          top: 0.1,
          bottom: 0.25,
        },
        borderVisible: true,
        entireTextOnly: false,
      },
      timeScale: {
        borderColor: '#2B3139',
        timeVisible: true,
        secondsVisible: false,
        borderVisible: true,
        rightOffset: 5,
        barSpacing: 8,
      },
      handleScroll: {
        mouseWheel: true,
        pressedMouseMove: true,
        horzTouchDrag: true,
        vertTouchDrag: true,
      },
      handleScale: {
        axisPressedMouseMove: true,
        mouseWheel: true,
        pinch: true,
      },
      localization: {
        timeFormatter: (time: number) => {
          const date = new Date(time * 1000)
          return date.toLocaleString('zh-CN', {
            month: '2-digit',
            day: '2-digit',
            hour: '2-digit',
            minute: '2-digit',
            hour12: false,
          })
        },
      },
    })

    chartRef.current = chart

    // 创建K线系列
    const candlestickSeries = chart.addSeries(CandlestickSeries, {
      upColor: '#0ECB81',
      downColor: '#F6465D',
      borderUpColor: '#0ECB81',
      borderDownColor: '#F6465D',
      wickUpColor: '#0ECB81',
      wickDownColor: '#F6465D',
    })
    candlestickSeriesRef.current = candlestickSeries as any

    // 创建成交量系列
    const volumeSeries = chart.addSeries(HistogramSeries, {
      color: '#26a69a',
      priceFormat: {
        type: 'volume',
      },
      priceScaleId: '',
      lastValueVisible: false,
      priceLineVisible: false,
    })
    volumeSeriesRef.current = volumeSeries as any

    // 响应式调整 (ResizeObserver)
    const resizeObserver = new ResizeObserver((entries) => {
      if (entries.length === 0 || !entries[0].contentRect) return
      const { width, height } = entries[0].contentRect
      chart.applyOptions({ width, height })
    })

    if (chartContainerRef.current) {
      resizeObserver.observe(chartContainerRef.current)
    }

    // 监听鼠标移动，显示 OHLC 信息
    chart.subscribeCrosshairMove((param) => {
      if (!param.time || !param.point || !candlestickSeriesRef.current) {
        setTooltipData(null)
        return
      }

      const data = param.seriesData.get(candlestickSeriesRef.current as any)
      if (!data) {
        setTooltipData(null)
        return
      }

      const candleData = data as any

      // 从存储的数据中获取 volume 和 quoteVolume
      const klineExtra = klineDataRef.current.get(param.time as number) || { volume: 0, quoteVolume: 0 }

      setTooltipData({
        time: param.time,
        open: candleData.open,
        high: candleData.high,
        low: candleData.low,
        close: candleData.close,
        volume: klineExtra.volume,
        quoteVolume: klineExtra.quoteVolume,
        x: param.point.x,
        y: param.point.y,
      })
    })

    return () => {
      resizeObserver.disconnect()
      chart.remove()
    }
  }, []) // Chart is created once, ResizeObserver handles dimension changes


  // 加载数据和指标
  useEffect(() => {
    // 当 symbol 或 interval 改变时，重置初始加载标志（以便自动适配新数据）
    isInitialLoadRef.current = true
    lastCandleTimeRef.current = 0

    // 清除旧的标记数据，避免旧数据影响新图表
    currentMarkersDataRef.current = []
    if (seriesMarkersRef.current) {
      try {
        seriesMarkersRef.current.setMarkers([])
      } catch (e) {
        // 忽略错误，稍后会重新创建
      }
      seriesMarkersRef.current = null
    }

    const loadData = async (isRefresh = false) => {
      if (!candlestickSeriesRef.current) return

      // 只在首次加载时显示 loading，刷新时不显示避免闪烁
      if (!isRefresh) {
        setLoading(true)
      }
      setError(null)

      try {
        // 1. 获取K线数据
        const klineData = await fetchKlineData(symbol, interval, isRefresh)

        // Check if chart is still mounted before updating
        if (!candlestickSeriesRef.current || !chartRef.current) return

        if (isRefresh) {
          // 刷新时只更新最新几根K线，跳过比已有数据更旧的K线避免报错
          klineData.forEach((k: any) => {
            if (k.time < lastCandleTimeRef.current) return
            candlestickSeriesRef.current!.update(k)
            if (k.time > lastCandleTimeRef.current) lastCandleTimeRef.current = k.time
            klineDataRef.current.set(k.time, { volume: k.volume || 0, quoteVolume: k.quoteVolume || 0 })
          })
        } else {
          candlestickSeriesRef.current.setData(klineData)
          // 存储 volume/quoteVolume 数据供 tooltip 使用
          klineDataRef.current.clear()
          klineData.forEach((k: any) => {
            klineDataRef.current.set(k.time, { volume: k.volume || 0, quoteVolume: k.quoteVolume || 0 })
          })
          // 记录最后一根K线时间
          if (klineData.length > 0) {
            lastCandleTimeRef.current = klineData[klineData.length - 1].time
          }
        }

        // 1.5 计算行情统计数据
        if (klineData.length > 1) {
          const latestKline = klineData[klineData.length - 1]
          const prevKline = klineData[klineData.length - 2]

          // 涨跌幅：当前K线收盘价 vs 前一根K线收盘价
          const priceChange = latestKline.close - prevKline.close
          const priceChangePercent = (priceChange / prevKline.close) * 100

          setMarketStats({
            price: latestKline.close,
            priceChange,
            priceChangePercent,
            high: latestKline.high,
            low: latestKline.low,
            volume: latestKline.volume || 0,
            quoteVolume: latestKline.quoteVolume || 0,
          })
        } else if (klineData.length === 1) {
          const latestKline = klineData[0]
          setMarketStats({
            price: latestKline.close,
            priceChange: 0,
            priceChangePercent: 0,
            high: latestKline.high,
            low: latestKline.low,
            volume: latestKline.volume || 0,
            quoteVolume: latestKline.quoteVolume || 0,
          })
        }

        // 2. 显示成交量
        if (volumeSeriesRef.current) {
          const volumeData = klineData.map((k: Kline) => ({
            time: k.time,
            value: k.volume || 0,
            color: k.close >= k.open ? 'rgba(14, 203, 129, 0.5)' : 'rgba(246, 70, 93, 0.5)',
          }))
          volumeSeriesRef.current.setData(volumeData)
        }

        // 3. 获取并显示订单标记（仅首次加载，刷新时跳过）
        if (!isRefresh && traderID && candlestickSeriesRef.current) {
          const orders = await fetchOrders(traderID, symbol)

          // Check if chart is still mounted after async operation
          if (!candlestickSeriesRef.current || !chartRef.current) return

          if (orders.length > 0) {
            // 提取 K 线时间数组（已排序）
            const klineTimes = klineData.map((k: any) => k.time as number)
            const klineMinTime = klineTimes[0] || 0
            const klineMaxTime = klineTimes[klineTimes.length - 1] || 0

            // 二分查找：找到订单时间所属的 K 线蜡烛
            // 返回 time <= orderTime 的最大 K 线时间
            const findCandleTime = (orderTime: number): number | null => {
              if (orderTime < klineMinTime || orderTime > klineMaxTime) {
                return null // 超出范围
              }

              let left = 0
              let right = klineTimes.length - 1

              while (left < right) {
                const mid = Math.ceil((left + right + 1) / 2)
                if (klineTimes[mid] <= orderTime) {
                  left = mid
                } else {
                  right = mid - 1
                }
              }

              return klineTimes[left]
            }

            // 按 K 线时间分组统计订单
            const ordersByCandle = new Map<number, { buys: number; sells: number }>()

            orders.forEach(order => {
              // 使用二分查找找到对应的 K 线蜡烛时间
              const candleTime = findCandleTime(order.time)

              if (candleTime === null) {
                return // 超出 K 线范围，正常跳过
              }

              const existing = ordersByCandle.get(candleTime) || { buys: 0, sells: 0 }
              if (order.rawSide === 'buy') {
                existing.buys++
              } else {
                existing.sells++
              }
              ordersByCandle.set(candleTime, existing)
            })

            // 为每个有订单的 K 线创建标记
            const markers: Array<{
              time: Time
              position: 'belowBar' | 'aboveBar'
              color: string
              shape: 'circle'
              text: string
              size: number
            }> = []

            ordersByCandle.forEach((counts, candleTime) => {
              // 显示买入标记（绿色，在K线下方）
              if (counts.buys > 0) {
                markers.push({
                  time: candleTime as Time,
                  position: 'belowBar' as const,
                  color: '#0ECB81',
                  shape: 'circle' as const,
                  text: counts.buys > 1 ? `B${counts.buys}` : 'B',
                  size: 1,
                })
              }
              // 显示卖出标记（红色，在K线上方）
              if (counts.sells > 0) {
                markers.push({
                  time: candleTime as Time,
                  position: 'aboveBar' as const,
                  color: '#F6465D',
                  shape: 'circle' as const,
                  text: counts.sells > 1 ? `S${counts.sells}` : 'S',
                  size: 1,
                })
              }
            })

            // 按时间排序（lightweight-charts 要求标记按时间顺序）
            markers.sort((a, b) => (a.time as number) - (b.time as number))

            try {
              // 存储标记数据供后续切换使用
              currentMarkersDataRef.current = markers

              // 使用 v5 API: createSeriesMarkers
              const markersToShow = showOrderMarkers ? markers : []

              if (seriesMarkersRef.current) {
                // 如果已经存在，更新标记
                seriesMarkersRef.current.setMarkers(markersToShow)
              } else {
                // 首次创建标记
                seriesMarkersRef.current = createSeriesMarkers(candlestickSeriesRef.current, markersToShow)
              }
            } catch (err) {
              console.error('[AdvancedChart] ❌ Failed to set markers:', err)
            }
          } else {
            try {
              if (seriesMarkersRef.current) {
                seriesMarkersRef.current.setMarkers([])
              }
            } catch (err) {
              console.error('[AdvancedChart] Failed to clear markers:', err)
            }
          }
        }

        // 只在初始加载时自动适配视图，避免刷新时抖动
        if (isInitialLoadRef.current) {
          chartRef.current?.timeScale().fitContent()
          isInitialLoadRef.current = false
        }
        setLoading(false)
      } catch (err: any) {
        console.error('[AdvancedChart] Error loading data:', err)
        setError(err.message || 'Failed to load chart data')
        setLoading(false)
      }
    }

    loadData(false) // 首次加载

    // 实时自动刷新 (15秒更新一次)
    const refreshInterval = setInterval(() => loadData(true), 15000)
    return () => clearInterval(refreshInterval)
  }, [symbol, interval, traderID, exchange])

  // 单独刷新挂单价格线 (60秒刷新一次，避免频繁调用交易所API)
  useEffect(() => {
    if (!traderID || !candlestickSeriesRef.current) return

    // 加载订单标记
    const loadOrderMarkers = async () => {
      if (!candlestickSeriesRef.current || !chartRef.current) return
      const orders = await fetchOrders(traderID, symbol)
      if (!candlestickSeriesRef.current) return

      if (orders.length === 0) return

      const klineData = Array.from(klineDataRef.current.keys()).sort((a, b) => a - b)
      if (klineData.length === 0) return

      const klineMinTime = klineData[0]
      const klineMaxTime = klineData[klineData.length - 1]

      const findCandleTime = (orderTime: number): number | null => {
        if (orderTime < klineMinTime || orderTime > klineMaxTime) return null
        let left = 0, right = klineData.length - 1
        while (left < right) {
          const mid = Math.ceil((left + right + 1) / 2)
          if (klineData[mid] <= orderTime) left = mid
          else right = mid - 1
        }
        return klineData[left]
      }

      const ordersByCandle = new Map<number, { buys: number; sells: number }>()
      orders.forEach(order => {
        const candleTime = findCandleTime(order.time)
        if (candleTime === null) return
        const existing = ordersByCandle.get(candleTime) || { buys: 0, sells: 0 }
        if (order.rawSide === 'buy') existing.buys++
        else existing.sells++
        ordersByCandle.set(candleTime, existing)
      })

      const markers: Array<{ time: Time; position: 'belowBar' | 'aboveBar'; color: string; shape: 'circle'; text: string; size: number }> = []
      ordersByCandle.forEach((counts, candleTime) => {
        if (counts.buys > 0) markers.push({ time: candleTime as Time, position: 'belowBar', color: '#0ECB81', shape: 'circle', text: counts.buys > 1 ? `B${counts.buys}` : 'B', size: 1 })
        if (counts.sells > 0) markers.push({ time: candleTime as Time, position: 'aboveBar', color: '#F6465D', shape: 'circle', text: counts.sells > 1 ? `S${counts.sells}` : 'S', size: 1 })
      })
      markers.sort((a, b) => (a.time as number) - (b.time as number))

      try {
        currentMarkersDataRef.current = markers
        const markersToShow = showOrderMarkers ? markers : []
        if (seriesMarkersRef.current) {
          seriesMarkersRef.current.setMarkers(markersToShow)
        } else if (candlestickSeriesRef.current) {
          seriesMarkersRef.current = createSeriesMarkers(candlestickSeriesRef.current, markersToShow)
        }
      } catch (err) {
        console.error('[AdvancedChart] Failed to refresh markers:', err)
      }
    }

    // 加载挂单并显示价格线
    const loadOpenOrders = async () => {
      try {
        // 先清除旧的价格线
        priceLinesRef.current.forEach(line => {
          try {
            candlestickSeriesRef.current?.removePriceLine(line)
          } catch (e) {
            // 忽略清除错误
          }
        })
        priceLinesRef.current = []

        const openOrders = await fetchOpenOrders(traderID, symbol)

        if (openOrders.length > 0 && candlestickSeriesRef.current) {
          openOrders.forEach(order => {
            // 获取触发价格 (止损/止盈用 stop_price，限价单用 price)
            const linePrice = order.stop_price > 0 ? order.stop_price : order.price
            if (linePrice <= 0) return

            // 判断订单类型
            const isStopLoss = order.type.includes('STOP') || order.type.includes('SL')
            const isTakeProfit = order.type.includes('TAKE_PROFIT') || order.type.includes('TP')
            const isLimit = order.type === 'LIMIT'

            // 设置价格线样式
            let lineColor = '#F0B90B' // 默认黄色
            const lineStyle = 2 // 虚线
            let title = ''

            if (isStopLoss) {
              lineColor = '#F6465D' // 红色 - 止损
              title = `SL ${order.quantity}`
            } else if (isTakeProfit) {
              lineColor = '#0ECB81' // 绿色 - 止盈
              title = `TP ${order.quantity}`
            } else if (isLimit) {
              lineColor = '#F0B90B' // 黄色 - 限价单
              title = `Limit ${order.side} ${order.quantity}`
            } else {
              title = `${order.type} ${order.quantity}`
            }

            const priceLine = candlestickSeriesRef.current?.createPriceLine({
              price: linePrice,
              color: lineColor,
              lineWidth: 1,
              lineStyle: lineStyle,
              axisLabelVisible: true,
              title: title,
            })

            if (priceLine) {
              priceLinesRef.current.push(priceLine)
            }
          })
        }
      } catch (err) {
        console.error('[AdvancedChart] Error loading open orders:', err)
      }
    }

    // 初始加载 (延迟1秒等待图表初始化完成)
    const initialTimeout = setTimeout(loadOpenOrders, 1000)

    // 60秒刷新一次挂单
    const openOrdersInterval = setInterval(loadOpenOrders, 60000)

    // 60秒刷新一次订单标记
    const markersInterval = setInterval(loadOrderMarkers, 60000)

    return () => {
      clearTimeout(initialTimeout)
      clearInterval(openOrdersInterval)
      clearInterval(markersInterval)
    }
  }, [symbol, traderID])

  // 单独处理订单标记的显示/隐藏，避免重新加载数据
  useEffect(() => {
    if (!seriesMarkersRef.current) return

    try {
      const markersToShow = showOrderMarkers ? currentMarkersDataRef.current : []
      seriesMarkersRef.current.setMarkers(markersToShow)
    } catch (err) {
      console.error('[AdvancedChart] ❌ Failed to toggle markers:', err)
    }
  }, [showOrderMarkers])

  return (
    <div
      className="relative shadow-xl"
      style={{
        background: 'linear-gradient(180deg, #0F1215 0%, #0B0E11 100%)',
        borderRadius: '12px',
        overflow: 'hidden',
        border: '1px solid rgba(43, 49, 57, 0.5)',
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Compact Professional Header */}
      <div
        className="flex items-center justify-between px-4 py-2"
        style={{ borderBottom: '1px solid rgba(43, 49, 57, 0.6)', background: '#0D1117', flexShrink: 0 }}
      >
        {/* Left: Symbol Info + Price */}
        <div className="flex items-center gap-4">
          {/* Symbol & Interval */}
          <div className="flex items-center gap-2">
            <span className="text-sm font-bold text-white">{symbol}</span>
            <span className="text-[10px] px-1.5 py-0.5 rounded bg-[#1F2937] text-gray-400">{interval}</span>
            <span
              className="text-[10px] px-1.5 py-0.5 rounded font-medium uppercase"
              style={{
                background: exchange === 'hyperliquid' ? 'rgba(80, 227, 194, 0.1)' : 'rgba(243, 186, 47, 0.1)',
                color: exchange === 'hyperliquid' ? '#50E3C2' : '#F3BA2F',
              }}
            >
              {exchange?.toUpperCase()}
            </span>
          </div>

          {/* Price Display */}
          {marketStats && (
            <div className="flex items-center gap-3 pl-3 border-l border-[#2B3139]">
              <span
                className="text-base font-bold tabular-nums"
                style={{ color: marketStats.priceChange >= 0 ? '#10B981' : '#EF4444' }}
              >
                {marketStats.price.toLocaleString(undefined, {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: exchange === 'forex' || exchange === 'metals' ? 4 : 2
                })}
              </span>
              <span
                className="text-xs font-medium px-1.5 py-0.5 rounded tabular-nums"
                style={{
                  background: marketStats.priceChange >= 0 ? 'rgba(16, 185, 129, 0.1)' : 'rgba(239, 68, 68, 0.1)',
                  color: marketStats.priceChange >= 0 ? '#10B981' : '#EF4444',
                }}
              >
                {marketStats.priceChange >= 0 ? '+' : ''}{marketStats.priceChangePercent.toFixed(2)}%
              </span>

              {/* Compact H/L */}
              <div className="flex items-center gap-2 text-[11px] text-gray-500">
                <span>H <span className="text-gray-300">{marketStats.high.toFixed(2)}</span></span>
                <span>L <span className="text-gray-300">{marketStats.low.toFixed(2)}</span></span>
                {marketStats.volume > 0 && baseUnit && (
                  <span>Vol <span className="text-gray-300">{formatVolume(marketStats.volume)}</span></span>
                )}
              </div>
            </div>
          )}
        </div>

        {/* Right: Controls */}
        <div className="flex items-center gap-1.5">
          {loading && (
            <span className="text-[10px] text-yellow-400 animate-pulse mr-2">
              {language === 'zh' ? '更新中...' : 'Updating...'}
            </span>
          )}

          <button
            onClick={() => setShowOrderMarkers(!showOrderMarkers)}
            className="flex items-center gap-1 px-2 py-1 rounded text-[11px] font-medium transition-all"
            style={{
              background: showOrderMarkers ? 'rgba(16, 185, 129, 0.15)' : 'transparent',
              color: showOrderMarkers ? '#10B981' : '#6B7280',
            }}
            title={language === 'zh' ? '订单标记' : 'Order Markers'}
          >
            <span>B/S</span>
          </button>
        </div>
      </div>

      {/* 图表容器 */}
      <div style={{ position: 'relative', flex: 1, minHeight: 0 }}>
        <div ref={chartContainerRef} style={{ height: '100%', width: '100%' }} />

        {/* OHLC Tooltip */}
        {tooltipData && (
          <div
            ref={tooltipRef}
            style={{
              position: 'absolute',
              left: '10px',
              top: '10px',
              padding: '8px 12px',
              background: 'rgba(15, 18, 21, 0.95)',
              border: '1px solid rgba(240, 185, 11, 0.3)',
              borderRadius: '6px',
              color: '#EAECEF',
              fontSize: '12px',
              fontFamily: 'monospace',
              pointerEvents: 'none',
              zIndex: 10,
              backdropFilter: 'blur(10px)',
              boxShadow: '0 4px 12px rgba(0, 0, 0, 0.5)',
            }}
          >
            <div style={{ marginBottom: '6px', color: '#F0B90B', fontWeight: 'bold', fontSize: '11px' }}>
              {new Date((tooltipData.time as number) * 1000).toLocaleString(language === 'zh' ? 'zh-CN' : 'en-US', {
                month: 'short',
                day: 'numeric',
                hour: '2-digit',
                minute: '2-digit',
              })}
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 12px', fontSize: '11px' }}>
              <span style={{ color: '#848E9C' }}>O:</span>
              <span style={{ color: '#EAECEF', fontWeight: '500' }}>{tooltipData.open?.toFixed(2)}</span>

              <span style={{ color: '#848E9C' }}>H:</span>
              <span style={{ color: '#0ECB81', fontWeight: '500' }}>{tooltipData.high?.toFixed(2)}</span>

              <span style={{ color: '#848E9C' }}>L:</span>
              <span style={{ color: '#F6465D', fontWeight: '500' }}>{tooltipData.low?.toFixed(2)}</span>

              <span style={{ color: '#848E9C' }}>C:</span>
              <span style={{
                color: tooltipData.close >= tooltipData.open ? '#0ECB81' : '#F6465D',
                fontWeight: 'bold'
              }}>
                {tooltipData.close?.toFixed(2)}
              </span>

              {tooltipData.volume > 0 && baseUnit && (
                <>
                  <span style={{ color: '#848E9C' }}>V({baseUnit}):</span>
                  <span style={{ color: '#3B82F6', fontWeight: '500' }}>
                    {formatVolume(tooltipData.volume)}
                  </span>
                </>
              )}

              {tooltipData.quoteVolume > 0 && quoteUnit && (
                <>
                  <span style={{ color: '#848E9C' }}>V({quoteUnit}):</span>
                  <span style={{ color: '#3B82F6', fontWeight: '500' }}>
                    {formatVolume(tooltipData.quoteVolume)}
                  </span>
                </>
              )}
            </div>
          </div>
        )}

        {/* NOFX 水印 */}
        <div
          style={{
            position: 'absolute',
            bottom: '20%',
            right: '5%',
            pointerEvents: 'none',
            userSelect: 'none',
            zIndex: 1,
          }}
        >
          <div
            style={{
              fontSize: '56px',
              fontWeight: '700',
              color: 'rgba(240, 185, 11, 0.12)',
              letterSpacing: '4px',
              fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, sans-serif',
              textShadow: '0 2px 30px rgba(240, 185, 11, 0.2)',
            }}
          >
            NOFX
          </div>
        </div>
      </div>

      {/* 错误提示 */}
      {error && (
        <div
          className="absolute inset-0 flex items-center justify-center"
          style={{ background: 'rgba(11, 14, 17, 0.9)' }}
        >
          <div className="text-center">
            <div className="text-2xl mb-2">⚠️</div>
            <div style={{ color: '#F6465D' }}>{error}</div>
          </div>
        </div>
      )}

    </div>
  )
}
