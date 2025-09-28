package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

type NotificationType int

const (
	Success NotificationType = iota
	Error
	Warning
)

type DiscordNotifier struct {
	webhookURL string
	onSuccess  bool
	onError    bool
	onWarning  bool
}

type DiscordWebhook struct {
	Content string                `json:"content,omitempty"`
	Embeds  []DiscordWebhookEmbed `json:"embeds,omitempty"`
}

type DiscordWebhookEmbed struct {
	Title       string                     `json:"title,omitempty"`
	Description string                     `json:"description,omitempty"`
	Color       int                        `json:"color,omitempty"`
	Fields      []DiscordWebhookEmbedField `json:"fields,omitempty"`
	Footer      *DiscordWebhookEmbedFooter `json:"footer,omitempty"`
	Timestamp   string                     `json:"timestamp,omitempty"`
}

type DiscordWebhookEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordWebhookEmbedFooter struct {
	Text string `json:"text"`
}

func NewDiscordNotifier(webhookURL string, onSuccess, onError, onWarning bool) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: webhookURL,
		onSuccess:  onSuccess,
		onError:    onError,
		onWarning:  onWarning,
	}
}

func (d *DiscordNotifier) SendBackupNotification(notifType NotificationType, service, operation string, details map[string]string, err error) {
	if !d.shouldSend(notifType) {
		return
	}

	var title, description string
	var color int

	switch notifType {
	case Success:
		title = "Backup Successful"
		description = fmt.Sprintf("Completed %s for service: **%s**", operation, service)
		color = 0x00ff00 // Green
	case Error:
		title = "Backup Failed"
		description = fmt.Sprintf("Failed to %s for service: **%s**", operation, service)
		if err != nil {
			description += fmt.Sprintf("\n\n**Error:** ```%s```", err.Error())
		}
		color = 0xff0000 // Red
	case Warning:
		title = "Backup Warning"
		description = fmt.Sprintf("Warning during %s for service: **%s**", operation, service)
		color = 0xffff00 // Yellow
	}

	embed := DiscordWebhookEmbed{
		Title:       title,
		Description: description,
		Color:       color,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer: &DiscordWebhookEmbedFooter{
			Text: "Stash Backup Tool",
		},
	}

	// Add detail fields
	for key, value := range details {
		embed.Fields = append(embed.Fields, DiscordWebhookEmbedField{
			Name:   key,
			Value:  value,
			Inline: true,
		})
	}

	webhook := DiscordWebhook{
		Embeds: []DiscordWebhookEmbed{embed},
	}

	if err := d.sendWebhook(webhook); err != nil {
		logrus.Errorf("Failed to send Discord notification: %v", err)
	}
}

func (d *DiscordNotifier) SendCleanupNotification(notifType NotificationType, deletedCount int, totalSize int64, err error) {
	if !d.shouldSend(notifType) {
		return
	}

	var title, description string
	var color int

	switch notifType {
	case Success:
		title = "Cleanup Successful"
		description = fmt.Sprintf("Cleaned up **%d** old backups", deletedCount)
		color = 0x00ff00 // Green
	case Error:
		title = "Cleanup Failed"
		description = "Failed to cleanup old backups"
		if err != nil {
			description += fmt.Sprintf("\n\n**Error:** ```%s```", err.Error())
		}
		color = 0xff0000 // Red
	case Warning:
		title = "Cleanup Warning"
		description = "Warning during cleanup operation"
		color = 0xffff00 // Yellow
	}

	embed := DiscordWebhookEmbed{
		Title:       title,
		Description: description,
		Color:       color,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer: &DiscordWebhookEmbedFooter{
			Text: "Stash Backup Tool",
		},
	}

	if deletedCount > 0 {
		embed.Fields = append(embed.Fields, DiscordWebhookEmbedField{
			Name:   "Deleted Backups",
			Value:  fmt.Sprintf("%d", deletedCount),
			Inline: true,
		})
	}

	if totalSize > 0 {
		embed.Fields = append(embed.Fields, DiscordWebhookEmbedField{
			Name:   "Space Freed",
			Value:  formatBytes(totalSize),
			Inline: true,
		})
	}

	webhook := DiscordWebhook{
		Embeds: []DiscordWebhookEmbed{embed},
	}

	if err := d.sendWebhook(webhook); err != nil {
		logrus.Errorf("Failed to send Discord notification: %v", err)
	}
}

func (d *DiscordNotifier) shouldSend(notifType NotificationType) bool {
	if d.webhookURL == "" {
		return false
	}

	switch notifType {
	case Success:
		return d.onSuccess
	case Error:
		return d.onError
	case Warning:
		return d.onWarning
	}

	return false
}

func (d *DiscordNotifier) sendWebhook(webhook DiscordWebhook) error {
	jsonData, err := json.Marshal(webhook)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook data: %w", err)
	}

	resp, err := http.Post(d.webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status code %d", resp.StatusCode)
	}

	logrus.Debug("Discord notification sent successfully")
	return nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
