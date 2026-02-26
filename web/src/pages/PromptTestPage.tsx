import { useState, useEffect } from 'react'
import { useAuth } from '../contexts/AuthContext'
import { useLanguage } from '../contexts/LanguageContext'
import {
  Bot,
  RefreshCw,
  Loader2,
  FileText,
  Play,
  Clock,
  Sparkles,
} from 'lucide-react'
import type { Strategy, AIModel } from '../types'
import { notify } from '../lib/notify'
import { DeepVoidBackground } from '../components/DeepVoidBackground'

const API_BASE = import.meta.env.VITE_API_BASE || ''

export function PromptTestPage() {
  const { token } = useAuth()
  const { language } = useLanguage()

  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [selectedStrategy, setSelectedStrategy] = useState<Strategy | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [, setError] = useState<string | null>(null)

  // Prompt Preview states
  const [activeTab, setActiveTab] = useState<'prompt' | 'test'>('prompt')
  const [promptPreview, setPromptPreview] = useState<{
    system_prompt: string
    user_prompt?: string
    prompt_variant: string
    config_summary: Record<string, unknown>
  } | null>(null)
  const [isLoadingPrompt, setIsLoadingPrompt] = useState(false)
  const [selectedVariant, setSelectedVariant] = useState('balanced')

  // AI Test states
  const [aiTestResult, setAiTestResult] = useState<{
    system_prompt?: string
    user_prompt?: string
    ai_response?: string
    reasoning?: string
    decisions?: string
    duration_ms?: number
  } | null>(null)
  const [isRunningTest, setIsRunningTest] = useState(false)
  const [selectedModelId, setSelectedModelId] = useState<string>('')
  const [aiModels, setAiModels] = useState<AIModel[]>([])

  const t = (key: string) => {
    const translations: Record<string, { zh: string; en: string }> = {
      title: { zh: 'Prompt 测试工具', en: 'Prompt Test Tool' },
      selectStrategy: { zh: '选择策略', en: 'Select Strategy' },
      noStrategies: { zh: '暂无策略', en: 'No strategies' },
      promptPreview: { zh: 'Prompt 预览', en: 'Prompt Preview' },
      aiTestRun: { zh: 'AI 测试', en: 'AI Test' },
      systemPrompt: { zh: 'System Prompt', en: 'System Prompt' },
      userPrompt: { zh: 'User Prompt', en: 'User Prompt' },
      loadPrompt: { zh: '生成 Prompt', en: 'Generate Prompt' },
      refreshPrompt: { zh: '刷新', en: 'Refresh' },
      promptVariant: { zh: '风格', en: 'Style' },
      balanced: { zh: '平衡', en: 'Balanced' },
      aggressive: { zh: '激进', en: 'Aggressive' },
      conservative: { zh: '保守', en: 'Conservative' },
      selectModel: { zh: '选择 AI 模型', en: 'Select AI Model' },
      runTest: { zh: '运行 AI 测试', en: 'Run AI Test' },
      running: { zh: '运行中...', en: 'Running...' },
      aiOutput: { zh: 'AI 输出', en: 'AI Output' },
      reasoning: { zh: '思维链', en: 'Reasoning' },
      decisions: { zh: '决策', en: 'Decisions' },
      duration: { zh: '耗时', en: 'Duration' },
      clickToGenerate: { zh: '点击生成 Prompt 预览', en: 'Click to generate prompt preview' },
      clickToRun: { zh: '点击运行 AI 测试', en: 'Click to run AI test' },
      noModels: { zh: '请先配置 AI 模型', en: 'Please configure AI models first' },
    }
    return translations[key]?.[language] || key
  }

  // Fetch strategies
  useEffect(() => {
    if (!token) return
    const fetchStrategies = async () => {
      try {
        const response = await fetch(`${API_BASE}/api/strategies`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!response.ok) throw new Error('Failed to fetch strategies')
        const data = await response.json()
        const list = data.strategies || []
        setStrategies(list)
        if (list.length > 0) {
          setSelectedStrategy(list[0])
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Unknown error')
      } finally {
        setIsLoading(false)
      }
    }
    fetchStrategies()
  }, [token])

  // Fetch AI models
  useEffect(() => {
    if (!token) return
    const fetchModels = async () => {
      try {
        const response = await fetch(`${API_BASE}/api/models`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!response.ok) throw new Error('Failed to fetch AI models')
        const data = await response.json()
        setAiModels(data)
        if (data.length > 0) {
          setSelectedModelId(data[0].id)
        }
      } catch (err) {
        console.error('Failed to fetch AI models:', err)
      }
    }
    fetchModels()
  }, [token])

  // Fetch prompt preview
  const fetchPromptPreview = async () => {
    if (!token || !selectedStrategy) return
    setIsLoadingPrompt(true)
    try {
      const response = await fetch(`${API_BASE}/api/strategies/preview-prompt`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          config: selectedStrategy.config,
          prompt_variant: selectedVariant,
        }),
      })
      if (!response.ok) throw new Error('Failed to fetch prompt preview')
      const data = await response.json()
      setPromptPreview(data)
    } catch (err) {
      notify.error(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setIsLoadingPrompt(false)
    }
  }

  // Run AI test
  const runAiTest = async () => {
    if (!token || !selectedStrategy || !selectedModelId) return
    setIsRunningTest(true)
    setAiTestResult(null)
    try {
      const response = await fetch(`${API_BASE}/api/strategies/test-ai`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          config: selectedStrategy.config,
          ai_model_id: selectedModelId,
          prompt_variant: selectedVariant,
        }),
      })
      if (!response.ok) throw new Error('Failed to run AI test')
      const data = await response.json()
      setAiTestResult(data)
    } catch (err) {
      notify.error(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setIsRunningTest(false)
    }
  }

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Loader2 className="w-8 h-8 animate-spin text-nofx-gold" />
      </div>
    )
  }

  return (
    <DeepVoidBackground>
      <div className="max-w-7xl mx-auto px-4 py-8">
        {/* Header */}
        <div className="mb-6">
          <h1 className="text-3xl font-bold text-nofx-text mb-2">{t('title')}</h1>
          <p className="text-nofx-text-secondary">
            {language === 'zh' ? '测试和预览策略的 AI Prompt' : 'Test and preview strategy AI prompts'}
          </p>
        </div>

        {/* Strategy Selector */}
        <div className="mb-6">
          <label className="block text-sm font-medium text-nofx-text mb-2">
            {t('selectStrategy')}
          </label>
          <select
            value={selectedStrategy?.id || ''}
            onChange={(e) => {
              const strategy = strategies.find(s => s.id === e.target.value)
              setSelectedStrategy(strategy || null)
              setPromptPreview(null)
              setAiTestResult(null)
            }}
            className="w-full max-w-md px-4 py-2 rounded-lg bg-nofx-bg-lighter border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold"
          >
            {strategies.length === 0 ? (
              <option>{t('noStrategies')}</option>
            ) : (
              strategies.map(strategy => (
                <option key={strategy.id} value={strategy.id}>
                  {strategy.name}
                </option>
              ))
            )}
          </select>
        </div>

        {/* Main Content */}
        {selectedStrategy && (
          <div className="bg-nofx-bg-lighter/50 backdrop-blur-sm rounded-lg border border-nofx-gold/20 overflow-hidden">
            {/* Tabs */}
            <div className="flex border-b border-nofx-gold/20">
              <button
                onClick={() => setActiveTab('prompt')}
                className={`flex-1 px-6 py-3 text-sm font-medium transition-colors ${
                  activeTab === 'prompt'
                    ? 'bg-purple-600/20 text-purple-400 border-b-2 border-purple-500'
                    : 'text-nofx-text-secondary hover:text-nofx-text hover:bg-nofx-bg-lighter/50'
                }`}
              >
                <div className="flex items-center justify-center gap-2">
                  <FileText className="w-4 h-4" />
                  {t('promptPreview')}
                </div>
              </button>
              <button
                onClick={() => setActiveTab('test')}
                className={`flex-1 px-6 py-3 text-sm font-medium transition-colors ${
                  activeTab === 'test'
                    ? 'bg-green-600/20 text-green-400 border-b-2 border-green-500'
                    : 'text-nofx-text-secondary hover:text-nofx-text hover:bg-nofx-bg-lighter/50'
                }`}
              >
                <div className="flex items-center justify-center gap-2">
                  <Play className="w-4 h-4" />
                  {t('aiTestRun')}
                </div>
              </button>
            </div>

            {/* Tab Content */}
            <div className="p-6">
              {activeTab === 'prompt' ? (
                /* Prompt Preview Tab */
                <div className="space-y-4">
                  {/* Controls */}
                  <div className="flex items-center gap-3 flex-wrap">
                    <select
                      value={selectedVariant}
                      onChange={(e) => setSelectedVariant(e.target.value)}
                      className="px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold"
                    >
                      <option value="balanced">{t('balanced')}</option>
                      <option value="aggressive">{t('aggressive')}</option>
                      <option value="conservative">{t('conservative')}</option>
                    </select>
                    <button
                      onClick={fetchPromptPreview}
                      disabled={isLoadingPrompt}
                      className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-colors disabled:opacity-50 bg-purple-600 hover:bg-purple-700 text-white"
                    >
                      {isLoadingPrompt ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                      {promptPreview ? t('refreshPrompt') : t('loadPrompt')}
                    </button>
                  </div>

                  {/* Preview Content */}
                  {promptPreview ? (
                    <div className="space-y-4">
                      <div>
                        <div className="flex items-center gap-2 mb-2">
                          <Sparkles className="w-4 h-4 text-purple-400" />
                          <h3 className="text-sm font-semibold text-nofx-text">{t('systemPrompt')}</h3>
                        </div>
                        <pre className="p-4 rounded-lg bg-nofx-bg border border-nofx-gold/10 text-xs text-nofx-text-secondary whitespace-pre-wrap overflow-x-auto max-h-96 overflow-y-auto">
                          {promptPreview.system_prompt}
                        </pre>
                      </div>
                      {promptPreview.user_prompt && (
                        <div>
                          <div className="flex items-center gap-2 mb-2">
                            <FileText className="w-4 h-4 text-blue-400" />
                            <h3 className="text-sm font-semibold text-nofx-text">{t('userPrompt')}</h3>
                          </div>
                          <pre className="p-4 rounded-lg bg-nofx-bg border border-nofx-gold/10 text-xs text-nofx-text-secondary whitespace-pre-wrap overflow-x-auto max-h-96 overflow-y-auto">
                            {promptPreview.user_prompt}
                          </pre>
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="flex flex-col items-center justify-center py-12 text-nofx-text-secondary">
                      <FileText className="w-12 h-12 mb-3 opacity-50" />
                      <p>{t('clickToGenerate')}</p>
                    </div>
                  )}
                </div>
              ) : (
                /* AI Test Tab */
                <div className="space-y-4">
                  {/* Controls */}
                  <div className="space-y-3">
                    <div className="flex items-center gap-3 flex-wrap">
                      <select
                        value={selectedVariant}
                        onChange={(e) => setSelectedVariant(e.target.value)}
                        className="px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold"
                      >
                        <option value="balanced">{t('balanced')}</option>
                        <option value="aggressive">{t('aggressive')}</option>
                        <option value="conservative">{t('conservative')}</option>
                      </select>
                      <select
                        value={selectedModelId}
                        onChange={(e) => setSelectedModelId(e.target.value)}
                        className="flex-1 min-w-[200px] px-3 py-2 rounded-lg bg-nofx-bg border border-nofx-gold/20 text-nofx-text outline-none focus:border-nofx-gold"
                      >
                        {aiModels.length === 0 ? (
                          <option>{t('noModels')}</option>
                        ) : (
                          aiModels.map(model => (
                            <option key={model.id} value={model.id}>
                              {model.name} ({model.provider})
                            </option>
                          ))
                        )}
                      </select>
                    </div>
                    <button
                      onClick={runAiTest}
                      disabled={isRunningTest || !selectedModelId}
                      className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-colors disabled:opacity-50 bg-green-600 hover:bg-green-700 text-white"
                    >
                      {isRunningTest ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4" />}
                      {isRunningTest ? t('running') : t('runTest')}
                    </button>
                  </div>

                  {/* Test Results */}
                  {aiTestResult ? (
                    <div className="space-y-4">
                      {aiTestResult.duration_ms && (
                        <div className="flex items-center gap-2 text-sm text-nofx-text-secondary">
                          <Clock className="w-4 h-4" />
                          {t('duration')}: {aiTestResult.duration_ms}ms
                        </div>
                      )}
                      {aiTestResult.ai_response && (
                        <div>
                          <div className="flex items-center gap-2 mb-2">
                            <Bot className="w-4 h-4 text-green-400" />
                            <h3 className="text-sm font-semibold text-nofx-text">{t('aiOutput')}</h3>
                          </div>
                          <pre className="p-4 rounded-lg bg-nofx-bg border border-nofx-gold/10 text-xs text-nofx-text-secondary whitespace-pre-wrap overflow-x-auto max-h-96 overflow-y-auto">
                            {aiTestResult.ai_response}
                          </pre>
                        </div>
                      )}
                      {aiTestResult.reasoning && (
                        <div>
                          <div className="flex items-center gap-2 mb-2">
                            <Sparkles className="w-4 h-4 text-purple-400" />
                            <h3 className="text-sm font-semibold text-nofx-text">{t('reasoning')}</h3>
                          </div>
                          <pre className="p-4 rounded-lg bg-nofx-bg border border-nofx-gold/10 text-xs text-nofx-text-secondary whitespace-pre-wrap overflow-x-auto max-h-96 overflow-y-auto">
                            {aiTestResult.reasoning}
                          </pre>
                        </div>
                      )}
                      {aiTestResult.decisions && (
                        <div>
                          <div className="flex items-center gap-2 mb-2">
                            <FileText className="w-4 h-4 text-blue-400" />
                            <h3 className="text-sm font-semibold text-nofx-text">{t('decisions')}</h3>
                          </div>
                          <pre className="p-4 rounded-lg bg-nofx-bg border border-nofx-gold/10 text-xs text-nofx-text-secondary whitespace-pre-wrap overflow-x-auto max-h-96 overflow-y-auto">
                            {aiTestResult.decisions}
                          </pre>
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="flex flex-col items-center justify-center py-12 text-nofx-text-secondary">
                      <Bot className="w-12 h-12 mb-3 opacity-50" />
                      <p>{t('clickToRun')}</p>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </DeepVoidBackground>
  )
}
