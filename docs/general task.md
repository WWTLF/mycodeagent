Хороший вопрос — давай разберём по пунктам.

---

### 1. Что уже есть в vLLM образе на Vast.ai

```
vllm/vllm-openai:latest уже содержит:
  ✅ Python 3.11
  ✅ vLLM
  ✅ CUDA драйверы
  ✅ huggingface_hub
  ✅ pip

❌ НЕ содержит:
  - твой HuggingFace токен
  - модель (качается при первом запуске)
```

---

### 2. Как vLLM скачивает модель

Самый простой способ — **vLLM скачивает сам** при старте если указать имя модели с HuggingFace:

```bash
# vLLM сам скачает модель при первом запуске
vllm serve Qwen/Qwen3-32B --port 8000
```

Модель кешируется в `/root/.cache/huggingface/` на диске инстанса.

---

### 3. Где токен HuggingFace

Нужен **только для приватных или gated моделей** (Llama 3, некоторые Mistral). Qwen3, Magnum, GLM — **публичные, токен не нужен**.

Если всё же нужен — передать через переменную среды:

```bash
# На удалённом инстансе перед запуском vLLM:
export HF_TOKEN="hf_xxxxxxxxxxxx"
vllm serve Qwen/Qwen3-32B --port 8000
```

---

### 4. Исправленный скрипт — чистый и понятный

```bash
#!/bin/bash
# setup-vastai.sh

set -e

INSTANCE_ID="${1:?Передай ID инстанса: $0 12345}"
MODEL="${2:-Qwen/Qwen3-32B}"          # модель по умолчанию
LOCAL_PORT=8000
HF_TOKEN="${HF_TOKEN:-}"              # пустой если не нужен

# ── 1. Старт инстанса ─────────────────────────────────────────────
echo "🚀 Запускаем инстанс $INSTANCE_ID..."
vastai start instance "$INSTANCE_ID" 2>/dev/null || true

echo "⏳ Ждём статус running..."
for i in $(seq 1 30); do
  STATUS=$(vastai show instance "$INSTANCE_ID" --raw | jq -r '.actual_status')
  [[ "$STATUS" == "running" ]] && echo "✅ Запущен" && break
  echo "  [${i}/30] $STATUS..."
  sleep 10
done
[[ "$STATUS" != "running" ]] && echo "❌ Не запустился" && exit 1

# ── 2. SSH параметры ──────────────────────────────────────────────
RAW=$(vastai show instance "$INSTANCE_ID" --raw)
SSH_IP=$(echo "$RAW"   | jq -r '.public_ipaddr')
SSH_PORT=$(echo "$RAW" | jq -r '.ports["22/tcp"][0].HostPort')
echo "📡 SSH: root@${SSH_IP}:${SSH_PORT}"

# Ждём SSH
echo "⏳ Ждём SSH..."
for i in $(seq 1 24); do
  ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 \
      -o BatchMode=yes -p "$SSH_PORT" "root@$SSH_IP" \
      "echo ok" 2>/dev/null && break
  sleep 5
done

# ── 3. Запуск vLLM на инстансе ────────────────────────────────────
# vllm/vllm-openai образ — Python и vLLM уже установлены
# Модель скачивается автоматически из HuggingFace при первом запуске
# и кешируется в /root/.cache/huggingface/

echo "🤖 Запускаем vLLM с моделью $MODEL..."
echo "   (первый запуск — скачает модель, ~10-20 минут)"

ssh -o StrictHostKeyChecking=no -p "$SSH_PORT" "root@$SSH_IP" "
  # Токен HuggingFace (нужен только для gated моделей)
  export HF_TOKEN='${HF_TOKEN}'
  export HUGGING_FACE_HUB_TOKEN='${HF_TOKEN}'

  # Убить старый vLLM если был
  pkill -f 'vllm serve' 2>/dev/null || true
  sleep 2

  # Запуск в фоне — vLLM сам скачает модель
  nohup vllm serve '${MODEL}' \
    --host 0.0.0.0 \
    --port 8000 \
    --max-model-len 32768 \
    --reasoning-parser qwen3 \
    --enable-auto-tool-choice \
    --tool-call-parser qwen3_coder \
    --gpu-memory-utilization 0.95 \
    --trust-remote-code \
    > /tmp/vllm.log 2>&1 &

  echo 'PID vLLM: '\$!

  # Ждём пока vLLM поднимется
  echo 'Ждём запуска (следим за логом)...'
  for i in \$(seq 1 60); do
    if curl -sf http://localhost:8000/health >/dev/null 2>&1; then
      echo '✅ vLLM готов!'
      break
    fi
    # Показываем прогресс скачивания
    tail -1 /tmp/vllm.log 2>/dev/null || true
    sleep 10
  done
"

# ── 4. SSH туннель ────────────────────────────────────────────────
echo "🔗 Открываем туннель localhost:${LOCAL_PORT}..."
pkill -f "ssh.*${LOCAL_PORT}:localhost:8000" 2>/dev/null || true

ssh -o StrictHostKeyChecking=no \
    -o ServerAliveInterval=30 \
    -N \
    -L "${LOCAL_PORT}:localhost:8000" \
    -p "$SSH_PORT" "root@$SSH_IP" &

echo "$!" > /tmp/vastai_tunnel.pid
echo "$INSTANCE_ID" > /tmp/vastai_instance.id
sleep 3

# Проверка
curl -sf "http://localhost:${LOCAL_PORT}/health" && \
  echo "✅ API доступен: http://localhost:${LOCAL_PORT}/v1" || \
  echo "⚠️  API ещё не готов — подожди и проверь сам"

echo ""
echo "Логи vLLM:  ssh -p $SSH_PORT root@$SSH_IP 'tail -f /tmp/vllm.log'"
echo "Остановить: ./stop-vastai.sh"
```

---

### 5. Использование

```bash
chmod +x setup-vastai.sh

# Qwen3-32B (токен не нужен — публичная)
./setup-vastai.sh 12345

# Magnum (тоже публичная)
./setup-vastai.sh 12345 "anthracite-org/magnum-v4-27b"

# Llama 3 (gated — нужен токен)
HF_TOKEN="hf_xxx" ./setup-vastai.sh 12345 "meta-llama/Llama-3.3-70B-Instruct"
```