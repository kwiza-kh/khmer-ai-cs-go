package api

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"khmer-ai-cs-go/internal/security"
)

// BackfillLegacySecrets re-seals migration-era plaintext secrets so the
// at-rest encryption guarantee converges. Migration 052 documents read-path
// re-sealing that was never implemented (the sealer passes unrecognised
// values through in both directions), so rows written before sealing held a
// working TOTP secret or channel credential in cleartext for any database
// read channel — backups, replicas, query logs — indefinitely unless the
// tenant happened to re-save. Idempotent: sealed values are skipped by the
// NOT LIKE 'enc:v1:%' predicate and Encrypt passes already-sealed values
// through unchanged. Returns the number of rows re-sealed.
func BackfillLegacySecrets(ctx context.Context, pool *pgxpool.Pool, sealer *security.Sealer) (int, error) {
	if sealer == nil {
		return 0, nil
	}
	sealed := 0

	totpRows, err := pool.Query(ctx, sqlSelectLegacyTOTP)
	if err != nil {
		return 0, err
	}
	type totpRow struct {
		UserID int32
		Secret string
	}
	var totp []totpRow
	for totpRows.Next() {
		var r totpRow
		if err := totpRows.Scan(&r.UserID, &r.Secret); err == nil {
			totp = append(totp, r)
		}
	}
	totpRows.Close()
	for _, r := range totp {
		enc, err := sealer.Encrypt(r.Secret)
		if err != nil {
			return sealed, err
		}
		if _, err := pool.Exec(ctx, sqlBackfillTOTP, enc, r.UserID); err != nil {
			return sealed, err
		}
		sealed++
	}

	cfgRows, err := pool.Query(ctx, sqlSelectLegacyConfigs)
	if err != nil {
		return sealed, err
	}
	type credRow struct {
		ConfigID  int64
		Column    string // fixed literal chosen below: access_token | bot_token | webhook_secret
		Plaintext string
	}
	var creds []credRow
	for cfgRows.Next() {
		var configID int64
		var access string
		var bot, webhook *string
		if err := cfgRows.Scan(&configID, &access, &bot, &webhook); err != nil {
			continue
		}
		if access != "" && !strings.HasPrefix(access, "enc:v1:") {
			creds = append(creds, credRow{ConfigID: configID, Column: "access_token", Plaintext: access})
		}
		if bot != nil && *bot != "" && !strings.HasPrefix(*bot, "enc:v1:") {
			creds = append(creds, credRow{ConfigID: configID, Column: "bot_token", Plaintext: *bot})
		}
		if webhook != nil && *webhook != "" && !strings.HasPrefix(*webhook, "enc:v1:") {
			creds = append(creds, credRow{ConfigID: configID, Column: "webhook_secret", Plaintext: *webhook})
		}
	}
	cfgRows.Close()
	for _, c := range creds {
		enc, err := sealer.Encrypt(c.Plaintext)
		if err != nil {
			return sealed, err
		}
		var stmt string
		switch c.Column {
		case "access_token":
			stmt = sqlBackfillAccess
		case "bot_token":
			stmt = sqlBackfillBot
		case "webhook_secret":
			stmt = sqlBackfillWebhook
		default:
			continue
		}
		if _, err := pool.Exec(ctx, stmt, enc, c.ConfigID); err != nil {
			return sealed, err
		}
		sealed++
	}

	// model_configs.api_key holds the platform-global provider key. It was
	// written in cleartext by updateModelConfig and is not in the sealing
	// regime above, so the same convergence applies to it.
	modelRows, err := pool.Query(ctx, sqlSelectLegacyModelKeys)
	if err != nil {
		return sealed, err
	}
	type modelRow struct {
		ConfigID int64
		APIKey   string
	}
	var models []modelRow
	for modelRows.Next() {
		var r modelRow
		if err := modelRows.Scan(&r.ConfigID, &r.APIKey); err == nil {
			models = append(models, r)
		}
	}
	modelRows.Close()
	for _, r := range models {
		enc, err := sealer.Encrypt(r.APIKey)
		if err != nil {
			return sealed, err
		}
		if _, err := pool.Exec(ctx, sqlBackfillModelKey, enc, r.ConfigID); err != nil {
			return sealed, err
		}
		sealed++
	}
	return sealed, nil
}

const sqlSelectLegacyTOTP = "SELECT user_id, secret FROM user_totp WHERE secret <> '' AND secret NOT LIKE 'enc:v1:%'"

const sqlBackfillTOTP = "UPDATE user_totp SET secret = $1 WHERE user_id = $2"

const sqlSelectLegacyConfigs = "SELECT config_id, access_token, COALESCE(bot_token,''), COALESCE(webhook_secret,'') FROM platform_configs " +
	"WHERE (access_token <> '' AND access_token NOT LIKE 'enc:v1:%') " +
	"OR (bot_token IS NOT NULL AND bot_token <> '' AND bot_token NOT LIKE 'enc:v1:%') " +
	"OR (webhook_secret IS NOT NULL AND webhook_secret <> '' AND webhook_secret NOT LIKE 'enc:v1:%')"

const sqlBackfillAccess = "UPDATE platform_configs SET access_token = $1 WHERE config_id = $2"

const sqlBackfillBot = "UPDATE platform_configs SET bot_token = $1 WHERE config_id = $2"

const sqlBackfillWebhook = "UPDATE platform_configs SET webhook_secret = $1 WHERE config_id = $2"

const sqlSelectLegacyModelKeys = "SELECT config_id, api_key FROM model_configs WHERE api_key <> '' AND api_key NOT LIKE 'enc:v1:%'"

const sqlBackfillModelKey = "UPDATE model_configs SET api_key = $1 WHERE config_id = $2"
