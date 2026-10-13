package i18n

import "fmt"

func fmtSprintf(s string, args []interface{}) string {
	defer func() { recover() }()
	return fmt.Sprintf(s, args...)
}

// English is the reference bundle. Keys mirror the legacy translation key set where
// applicable (error keys keep the original names so translated files import).
var English = map[string]string{
	"login":                   "Login",
	"logout":                  "Logout",
	"username":                "Username",
	"password":                "Password",
	"dashboard":               "Dashboard",
	"servers":                 "Servers",
	"users":                   "Users",
	"roles":                   "Roles",
	"settings":                "Settings",
	"language":                "Language",
	"save":                    "Save",
	"cancel":                  "Cancel",
	"delete":                  "Delete",
	"edit":                    "Edit",
	"create":                  "Create",
	"start":                   "Start",
	"stop":                    "Stop",
	"restart":                 "Restart",
	"kill":                    "Kill",
	"running":                 "Running",
	"offline":                 "Offline",
	"crashed":                 "Crashed",
	"updating":                "Updating",
	"terminal":                "Terminal",
	"files":                   "Files",
	"backups":                 "Backups",
	"schedules":               "Schedules",
	"config":                  "Config",
	"stats":                   "Stats",
	"players":                 "Players",
	"webhooks":                "Webhooks",
	"NOT_AUTHORIZED":          "You are not authorized to access this resource.",
	"INVALID_JSON":            "The request body is not valid JSON.",
	"INVALID_JSON_SCHEMA":     "The request body does not match the expected schema.",
	"TRAVERSAL DETECTED":      "Path traversal detected. This incident has been reported.",
	"INVALID_TOKEN":           "Your session token is invalid or expired.",
	"USER_NOT_FOUND":          "User not found.",
	"SERVER_NOT_FOUND":        "Server not found.",
	"BACKUP_NOT_FOUND":        "Backup configuration not found.",
	"ALREADY_EXISTS":          "A record with this name already exists.",
	"BAD_PASSWORD":            "Incorrect username or password.",
	"ACCOUNT_DISABLED":        "This account is disabled.",
	"MFA_REQUIRED":            "Multi-factor authentication code required.",
	"INVALID_MFA":             "Invalid multi-factor authentication code.",
	"LOCKED_OUT":              "Too many failed attempts. Account temporarily locked.",
	"EULA_AGREEMENT_REQUIRED": "You must agree to the Minecraft EULA before starting this server.",
}

func init() { Register("en", English) }
