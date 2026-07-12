package usernames

import (
	"fmt"
	"sort"
	"strings"
)

const (
	MinLength = 4
	MaxLength = 63
)

// reservedGroups is the source of truth for names that may not be claimed by
// self-service personal-account signup. Entries are canonical username slugs:
// lower-case ASCII with words separated by hyphens. Administratively verified
// accounts can be provisioned separately if Gitslice later supports that flow.
var reservedGroups = []struct {
	category string
	names    []string
}{
	{
		category: "system and trust",
		names: []string{
			"admin", "administrator", "auth", "billing", "build",
			"contact", "docs", "help", "internal", "login", "logout",
			"moderator", "official", "president", "root", "security", "shared",
			"signup", "staff", "status", "support", "system", "verified",
			"webmaster", "whitehouse",
		},
	},
	{
		category: "gitslice",
		names: []string{
			"gitslice", "gitslice-admin", "gitslice-api", "gitslice-bot",
			"gitslice-ci", "gitslice-docs", "gitslice-security",
			"gitslice-status", "gitslice-support",
		},
	},
	{
		category: "well-known brands and platforms",
		names: []string{
			"adobe", "airbnb", "alibaba", "amazon", "anthropic", "apple",
			"atlassian", "cloudflare", "coca-cola", "coinbase", "discord",
			"docker", "dropbox", "facebook", "figma", "github", "gitlab",
			"google", "instagram", "intel", "linkedin", "mastercard",
			"meta", "microsoft", "netflix", "nike", "nintendo", "nvidia",
			"openai", "oracle", "paypal", "reddit", "salesforce", "samsung",
			"shopify", "slack", "sony", "spotify", "stripe", "telegram",
			"tesla", "tiktok", "twitch", "twitter", "uber", "vercel", "visa",
			"whatsapp", "wikipedia", "youtube",
		},
	},
	{
		category: "well-known public figures",
		names: []string{
			"barack-obama", "barackobama", "beyonce", "bill-gates", "billgates",
			"cristiano-ronaldo", "cristianoronaldo", "donald-trump", "donaldtrump",
			"dwayne-johnson", "elon-musk", "elonmusk", "jeff-bezos", "jeffbezos",
			"joe-biden", "joebiden", "kamala-harris", "kamalaharris",
			"kim-kardashian", "lady-gaga", "lebron-james", "lionel-messi",
			"mark-zuckerberg", "mrbeast", "oprah-winfrey", "pewdiepie", "rihanna",
			"sam-altman", "sama", "satya-nadella", "sundar-pichai",
			"taylor-swift", "taylorswift", "therock", "tim-cook", "tom-cruise",
		},
	},
}

var reserved = func() map[string]struct{} {
	names := make(map[string]struct{})
	for _, group := range reservedGroups {
		for _, name := range group.names {
			if _, exists := names[name]; exists {
				panic("duplicate reserved username: " + name)
			}
			names[name] = struct{}{}
		}
	}
	return names
}()

// Normalize returns the canonical personal-account slug for a username.
func Normalize(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	username = strings.ReplaceAll(username, "_", "-")
	if username == "" {
		return "", fmt.Errorf("username is required")
	}
	if len(username) > MaxLength {
		return "", fmt.Errorf("username must be %d characters or fewer", MaxLength)
	}
	if strings.HasPrefix(username, "-") || strings.HasSuffix(username, "-") {
		return "", fmt.Errorf("username must not start or end with '-'")
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("username may contain only letters, numbers, '-' or '_'")
	}
	if len(username) < MinLength {
		return "", fmt.Errorf("username must be at least %d characters", MinLength)
	}
	if IsReserved(username) {
		return "", fmt.Errorf("username is reserved")
	}
	return username, nil
}

func IsReserved(username string) bool {
	_, ok := reserved[username]
	return ok
}

// ReservedNames returns a sorted copy of the self-service reservation list.
func ReservedNames() []string {
	names := make([]string, 0, len(reserved))
	for name := range reserved {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
