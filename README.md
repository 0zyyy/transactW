# transactW

WhatsApp bot that automatically logs financial transactions using AI. Kinda like a chatbot, but not necessarily a chatbot

## Architecture

```
WhatsApp
   │
   ▼
Go Bot Gateway      ← webhook, conversation state, persistence
   │ HTTP
   ▼
Python Inference    ← Gemini LLM + OCR (doctr)
   │
   ▼
PostgreSQL
```

| Directory | Language | Role |
|---|---|---|
| `cmd/bot-gateway` | Go | Meta Cloud API webhook handler |
| `cmd/whatsmeow-gateway` | Go | Self-hosted WhatsApp via whatsmeow |
| `services/inference` | Python | Text & receipt parsing (Gemini + doctr) |
| `internal/` | Go | Shared config, persistence, conversation, reply |

## Usage

**1. Configure**

```bash
cp .env.example .env
# Fill in GEMINI_API_KEY, DATABASE_URL, and (if using Meta) WHATSAPP_ACCESS_TOKEN
```

**2. Start the inference service**

```bash
cd services/inference
pip install -r requirements.txt
python app.py
```

**3. Run the bot gateway**

```bash
# Meta Cloud API mode
go run ./cmd/bot-gateway

# Self-hosted mode (scan QR code on first run)
go run ./cmd/whatsmeow-gateway
```

**4. Register the webhook** _(Meta Cloud API only)_

Point `https://<your-domain>/webhook/whatsapp` in Meta for Developers and subscribe to the **messages** field.
