package main

import (
	"fmt"
	"strings"
	"time"
)

const (
	colorNew     = 0x2ECC71
	colorAmended = 0x3498DB
	colorSummary = 0x95A5A6
)

// searchPageURL is where a person goes to look the certification up themselves.
// The register has no per-certificate URL -- its detail view is a POST into a
// servlet -- so this is the search form, and the 番号 in the embed is what gets
// typed into it.
const searchPageURL = "https://www.tele.soumu.go.jp/giteki/SearchServlet?pageID=js01"

// buildPayload renders one change as a Discord webhook post.
func buildPayload(watched target, item change, apiBaseURL, mentionRoleID string, now time.Time, lastUpdateDate string) webhookPayload {
	certificate := item.Certificate

	title := fmt.Sprintf("🆕 %s の技適が登録されました", watched.displayLabel())
	color := colorNew
	if item.Kind == changeAmended {
		title = fmt.Sprintf("📝 %s の技適が更新されました", watched.displayLabel())
		color = colorAmended
	}
	if model := certificate.TypeName(); model != "" {
		title += "： " + model
	}

	fields := make([]embedField, 0, discordEmbedFieldLimit)
	fields = appendField(fields, "番号", certificate.Number, true)
	fields = appendField(fields, "年月日", certificate.Date(), true)
	fields = appendField(fields, "種類", strings.Join(certificate.TechCodes(), "\n"), true)
	fields = appendField(fields, "氏名又は名称", certificate.ApplicantName(), false)
	fields = appendField(fields, "特定無線設備の種別", bulletList(certificate.EquipmentKinds()), false)
	fields = appendField(fields, "電波の型式、周波数及び空中線電力", bulletList(certificate.ElecWaves()), false)
	fields = appendField(fields, "認証機関", strings.Join(certificate.OrganNames(), "\n"), true)

	if item.Kind == changeAmended {
		fields = appendField(fields, "工事設計", fmt.Sprintf("%d 件 → %d 件", item.PreviousRecordCount, len(certificate.Records)), true)
	}

	// 備考 is where the register writes the amendment history, so it is the one
	// field that says what actually changed on an amendment. It is also empty on
	// most new certifications, which is why it is added last rather than given a
	// fixed place.
	fields = appendField(fields, "備考", bulletList(certificate.Notes()), false)

	links := make([]string, 0, 2)
	if attachment := certificate.ExteriorAttachmentURL(apiBaseURL); attachment != "" {
		links = append(links, fmt.Sprintf("[外観写真等 (PDF)](%s)", attachment))
	}
	links = append(links, fmt.Sprintf("[技適検索](%s)", searchPageURL))
	fields = appendField(fields, "リンク", strings.Join(links, " · "), false)

	payload := webhookPayload{
		Username: discordUsername,
		Embeds: []embed{{
			Title:     truncate(title, discordEmbedTitleLimit),
			URL:       certificate.ExteriorAttachmentURL(apiBaseURL),
			Color:     color,
			Fields:    fields,
			Footer:    &embedFooter{Text: truncate(footerText(lastUpdateDate), discordEmbedFooterTextLimit)},
			Timestamp: now.UTC().Format(time.RFC3339),
		}},
		// parse is always present and always empty: it is what stops a product
		// name or a 備考 line that happens to contain @everyone from pinging the
		// server. The role below is allowed through by id, which parse cannot
		// do.
		AllowedMentions: allowedMentions{Parse: []string{}},
	}

	// Only a new certification pings. An amendment is a product that was already
	// announced changing its paperwork, which nobody needs to know within the
	// hour.
	if item.Kind == changeNew && mentionRoleID != "" {
		payload.Content = truncate(fmt.Sprintf("<@&%s>", mentionRoleID), discordContentLimit)
		payload.AllowedMentions.Roles = []string{mentionRoleID}
	}

	return payload
}

// buildSummaryPayload reports the certifications a run found but did not post
// one by one. It exists for the run after a long outage, and for the deliberate
// state reset that re-reports a target: without it those runs would be a few
// dozen messages in a row.
func buildSummaryPayload(watched target, held []change, now time.Time, lastUpdateDate string) webhookPayload {
	lines := make([]string, 0, len(held))
	for _, item := range held {
		model := item.Certificate.TypeName()
		if model == "" {
			model = "(型式不明)"
		}
		lines = append(lines, fmt.Sprintf("- %s　%s　%s", item.Certificate.Date(), item.Certificate.Number, model))
	}

	return webhookPayload{
		Username: discordUsername,
		Embeds: []embed{{
			Title: truncate(fmt.Sprintf("%s の技適が他に %d 件あります", watched.displayLabel(), len(held)), discordEmbedTitleLimit),
			URL:   searchPageURL,
			Description: truncate(
				"1回の実行で個別に投稿する上限を超えたので、残りはまとめて報告します。\n"+strings.Join(lines, "\n"),
				discordEmbedDescriptionLimit,
			),
			Color:     colorSummary,
			Footer:    &embedFooter{Text: truncate(footerText(lastUpdateDate), discordEmbedFooterTextLimit)},
			Timestamp: now.UTC().Format(time.RFC3339),
		}},
		AllowedMentions: allowedMentions{Parse: []string{}},
	}
}

func footerText(lastUpdateDate string) string {
	if strings.TrimSpace(lastUpdateDate) == "" {
		return "tele.soumu.go.jp"
	}
	return "tele.soumu.go.jp · データ更新日 " + lastUpdateDate
}

// appendField adds a field unless it has no value, and unless the embed is
// already at Discord's 25-field limit -- which Discord rejects the whole message
// for rather than trimming.
func appendField(fields []embedField, name, value string, inline bool) []embedField {
	if strings.TrimSpace(value) == "" || len(fields) >= discordEmbedFieldLimit {
		return fields
	}
	return append(fields, embedField{
		Name:   truncate(name, discordEmbedFieldNameLimit),
		Value:  truncate(value, discordEmbedFieldValueLimit),
		Inline: inline,
	})
}

func bulletList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	if len(values) == 1 {
		return values[0]
	}

	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, "- "+value)
	}
	return strings.Join(lines, "\n")
}
