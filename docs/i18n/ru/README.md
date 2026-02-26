<h1 align="center">NOFX — Open Source AI Торговая ОС</h1>

<p align="center">
  <strong>Инфраструктурный слой для AI-powered финансовой торговли</strong>
</p>

<p align="center">
  <a href="https://github.com/NoFxAiOS/nofx/stargazers"><img src="https://img.shields.io/github/stars/NoFxAiOS/nofx?style=for-the-badge" alt="Stars"></a>
  <a href="https://github.com/NoFxAiOS/nofx/releases"><img src="https://img.shields.io/github/v/release/NoFxAiOS/nofx?style=for-the-badge" alt="Release"></a>
  <a href="https://github.com/NoFxAiOS/nofx/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-AGPL--3.0-blue.svg?style=for-the-badge" alt="License"></a>
  <a href="https://t.me/nofx_dev_community"><img src="https://img.shields.io/badge/Telegram-Community-blue?style=for-the-badge&logo=telegram" alt="Telegram"></a>
</p>

<p align="center">
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go" alt="Go"></a>
  <a href="https://reactjs.org/"><img src="https://img.shields.io/badge/React-18+-61DAFB?style=flat&logo=react" alt="React"></a>
  <a href="https://www.typescriptlang.org/"><img src="https://img.shields.io/badge/TypeScript-5.0+-3178C6?style=flat&logo=typescript" alt="TypeScript"></a>
</p>

**Языки:** [English](../../../README.md) | [中文](../zh-CN/README.md) | [Русский](README.md)

---

### Основные функции

- **Мульти-AI поддержка**: Запускайте DeepSeek, Qwen, GPT, Claude, Gemini, Grok, Kimi — переключайтесь между моделями в любое время
- **Мульти-биржа**: Торгуйте на Binance, Bybit, OKX, Bitget, KuCoin, Gate, Hyperliquid, Aster DEX, Lighter с единой платформы
- **Студия стратегий**: Визуальный конструктор стратегий с источниками монет, индикаторами и контролем рисков
- **Режим AI-соревнования**: Несколько AI трейдеров соревнуются в реальном времени, отслеживание эффективности бок о бок
- **Веб-конфигурация**: Без редактирования JSON — настройка всего через веб-интерфейс
- **Панель реального времени**: Живые позиции, отслеживание P/L, логи решений AI с цепочкой рассуждений

### Официальные ссылки

- **Официальный сайт**: [https://nofxai.com](https://nofxai.com)
- **Панель данных**: [https://nofxos.ai/dashboard](https://nofxos.ai/dashboard)
- **Документация API**: [https://nofxos.ai/api-docs](https://nofxos.ai/api-docs)

> **Предупреждение о рисках**: Эта система экспериментальная. AI автоторговля несёт значительные риски. Настоятельно рекомендуется использовать только для обучения/исследований или тестирования с небольшими суммами!

## Сообщество разработчиков

Присоединяйтесь к Telegram сообществу: **[NOFX Developer Community](https://t.me/nofx_dev_community)**

---

## Перед началом

Для использования NOFX вам понадобится:

1. **Аккаунт биржи** - Зарегистрируйтесь на поддерживаемой бирже и создайте API ключи с правами торговли
2. **API ключ AI модели** - Получите от любого поддерживаемого провайдера (рекомендуется DeepSeek для экономии)

---

## Поддерживаемые биржи

### CEX (Централизованные биржи)

| Биржа | Статус | Регистрация (скидка) |
|----------|--------|-------------------------|
| **Binance** | ✅ Поддерживается | [Регистрация](https://www.binance.com/join?ref=NOFXENG) |
| **Bybit** | ✅ Поддерживается | [Регистрация](https://partner.bybit.com/b/83856) |
| **OKX** | ✅ Поддерживается | [Регистрация](https://www.okx.com/join/1865360) |
| **Bitget** | ✅ Поддерживается | [Регистрация](https://www.bitget.com/referral/register?from=referral&clacCode=c8a43172) |
| **KuCoin** | ✅ Поддерживается | [Регистрация](https://www.kucoin.com/r/broker/CXEV7XKK) |
| **Gate** | ✅ Поддерживается | [Регистрация](https://www.gatenode.xyz/share/VQBGUAxY) |

### Perp-DEX (Децентрализованные биржи)

| Биржа | Статус | Регистрация (скидка) |
|----------|--------|-------------------------|
| **Hyperliquid** | ✅ Поддерживается | [Регистрация](https://app.hyperliquid.xyz/join/AITRADING) |
| **Aster DEX** | ✅ Поддерживается | [Регистрация](https://www.asterdex.com/en/referral/fdfc0e) |
| **Lighter** | ✅ Поддерживается | [Регистрация](https://app.lighter.xyz/?referral=68151432) |

---

## Поддерживаемые AI модели

| AI Модель | Статус | Получить API ключ |
|----------|--------|-------------|
| **DeepSeek** | ✅ Поддерживается | [Получить](https://platform.deepseek.com) |
| **Qwen** | ✅ Поддерживается | [Получить](https://dashscope.console.aliyun.com) |
| **OpenAI (GPT)** | ✅ Поддерживается | [Получить](https://platform.openai.com) |
| **Claude** | ✅ Поддерживается | [Получить](https://console.anthropic.com) |
| **Gemini** | ✅ Поддерживается | [Получить](https://aistudio.google.com) |
| **Grok** | ✅ Поддерживается | [Получить](https://console.x.ai) |
| **Kimi** | ✅ Поддерживается | [Получить](https://platform.moonshot.cn) |

---

## Быстрый старт

### Вариант 1: Docker развёртывание (рекомендуется)

```bash
git clone https://github.com/de-rk/nofx.git
cd nofx
chmod +x ./start.sh
./start.sh start --build
```

Доступ к веб-интерфейсу: **http://localhost:3000**

### Обновление до последней версии

> **💡 Обновления выходят часто.** Запускайте эту команду ежедневно для получения последних функций и исправлений:

```bash
curl -fsSL https://raw.githubusercontent.com/de-rk/nofx/dev/install.sh | bash
```

Эта команда загружает последние официальные образы и автоматически перезапускает сервисы.

### Вариант 2: Ручная установка

```bash
# Требования: Go 1.21+, Node.js 18+, TA-Lib

# Установка TA-Lib (macOS)
brew install ta-lib

# Клонирование и настройка
git clone https://github.com/de-rk/nofx.git
cd nofx
go mod download
cd web && npm install && cd ..

# Запуск бэкенда
go build -o nofx && ./nofx

# Запуск фронтенда (новый терминал)
cd web && npm run dev
```

---

## Начальная настройка

1. **Настройка AI моделей** — Добавьте API ключи AI
2. **Настройка бирж** — Установите API учётные данные бирж
3. **Создание стратегии** — Настройте торговую стратегию в Студии стратегий
4. **Создание трейдера** — Объедините AI модель + Биржу + Стратегию
5. **Начало торговли** — Запустите настроенных трейдеров

---

## Предупреждения о рисках

1. Криптовалютные рынки крайне волатильны — AI решения не гарантируют прибыль
2. Торговля фьючерсами использует плечо — убытки могут превысить депозит
3. Экстремальные рыночные условия могут привести к ликвидации

---

## Лицензия

**GNU Affero General Public License v3.0 (AGPL-3.0)**

---

## Контакты

- **GitHub Issues**: [Создать Issue](https://github.com/NoFxAiOS/nofx/issues)
- **Сообщество разработчиков**: [Telegram группа](https://t.me/nofx_dev_community)
