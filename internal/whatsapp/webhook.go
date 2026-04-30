package whatsapp

type WebhookPayload struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

type Entry struct {
	ID      string   `json:"id"`
	Changes []Change `json:"changes"`
}

type Change struct {
	Field string `json:"field"`
	Value Value  `json:"value"`
}

type Value struct {
	MessagingProduct string    `json:"messaging_product"`
	Metadata         Metadata  `json:"metadata"`
	Contacts         []Contact `json:"contacts"`
	Messages         []Message `json:"messages"`
}

type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

type Contact struct {
	WaID    string         `json:"wa_id"`
	Profile ContactProfile `json:"profile"`
}

type ContactProfile struct {
	Name string `json:"name"`
}

type Message struct {
	From      string       `json:"from"`
	ID        string       `json:"id"`
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Text      *MessageText `json:"text"`
}

type MessageText struct {
	Body string `json:"body"`
}

type InboundText struct {
	WabaID        string `json:"waba_id"`
	PhoneNumberID string `json:"phone_number_id"`
	From          string `json:"from"`
	MessageID     string `json:"message_id"`
	Timestamp     string `json:"timestamp"`
	Text          string `json:"text"`
}

func ExtractInboundText(payload WebhookPayload) []InboundText {
	var messages []InboundText
	for _, entry := range payload.Entry {
		_ = entry.ID
		for _, change := range entry.Changes {
			for _, msg := range change.Value.Messages {
				if msg.Type != "text" || msg.Text == nil {
					continue
				}
				messages = append(messages, InboundText{
					WabaID:        entry.ID,
					PhoneNumberID: change.Value.Metadata.PhoneNumberID,
					From:          msg.From,
					MessageID:     msg.ID,
					Timestamp:     msg.Timestamp,
					Text:          msg.Text.Body,
				})
			}
		}
	}
	return messages
}
