package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Supported channel kinds.
const (
	ChannelWebhook  = "webhook"
	ChannelWeCom    = "wecom"
	ChannelTelegram = "telegram"
)

// SupportedChannels lists selectable channel kinds.
func SupportedChannels() []string {
	return []string{ChannelWebhook, ChannelWeCom, ChannelTelegram}
}

// ChannelLabel returns a human-readable Chinese label.
func ChannelLabel(c string) string {
	switch c {
	case ChannelWeCom:
		return "企业微信机器人"
	case ChannelTelegram:
		return "Telegram Bot"
	default:
		return "通用 Webhook"
	}
}

// payload is the rendered HTTP request for one event.
type payload struct {
	URL  string
	Body []byte
}

// PlainText renders an event exactly as it will be delivered. Exported so
// the admin report preview can show the real outbound message.
func PlainText(ev Event) string { return plainText(ev) }

// plainText renders an event as a readable multi-line message.
func plainText(ev Event) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(LevelLabel(ev.Level))
	b.WriteString("] ")
	b.WriteString(ev.Title)
	if strings.TrimSpace(ev.Text) != "" {
		b.WriteString("\n")
		b.WriteString(ev.Text)
	}
	for _, f := range ev.Fields {
		b.WriteString("\n")
		b.WriteString(f.Key)
		b.WriteString("：")
		b.WriteString(f.Value)
	}
	if !ev.Time.IsZero() {
		b.WriteString("\n时间：")
		b.WriteString(ev.Time.Format("2006-01-02 15:04:05"))
	}
	return b.String()
}

// render builds the channel-specific request payload.
//
// target is the webhook URL for webhook/wecom. For telegram it is the bot
// token, and extra carries the chat id.
func render(channel, target, extra string, ev Event) (payload, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return payload{}, fmt.Errorf("notify target is empty")
	}
	text := plainText(ev)

	switch channel {
	case ChannelWeCom:
		body, err := json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": text},
		})
		if err != nil {
			return payload{}, err
		}
		return payload{URL: target, Body: body}, nil

	case ChannelTelegram:
		chatID := strings.TrimSpace(extra)
		if chatID == "" {
			return payload{}, fmt.Errorf("telegram chat id is empty")
		}
		// target is the bot token; build the sendMessage endpoint.
		url := "https://api.telegram.org/bot" + target + "/sendMessage"
		body, err := json.Marshal(map[string]any{
			"chat_id":                  chatID,
			"text":                     text,
			"disable_web_page_preview": true,
		})
		if err != nil {
			return payload{}, err
		}
		return payload{URL: url, Body: body}, nil

	default: // ChannelWebhook
		fields := make(map[string]string, len(ev.Fields))
		for _, f := range ev.Fields {
			fields[f.Key] = f.Value
		}
		body, err := json.Marshal(map[string]any{
			"product": "sub2api-ext",
			"type":    ev.Type,
			"level":   ev.Level,
			"title":   ev.Title,
			"text":    ev.Text,
			"message": text,
			"fields":  fields,
			"time":    ev.Time.Format("2006-01-02T15:04:05Z07:00"),
		})
		if err != nil {
			return payload{}, err
		}
		return payload{URL: target, Body: body}, nil
	}
}
