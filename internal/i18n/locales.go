package i18n

// Additional locale bundles — core UI strings. Community translations extend
// these via frontend locale files; the backend bundle covers API error strings.
var German = map[string]string{
	"login": "Anmelden", "logout": "Abmelden", "username": "Benutzername",
	"password": "Passwort", "dashboard": "Übersicht", "servers": "Server",
	"users": "Benutzer", "roles": "Rollen", "settings": "Einstellungen",
	"save": "Speichern", "cancel": "Abbrechen", "delete": "Löschen", "edit": "Bearbeiten",
	"create": "Erstellen", "start": "Starten", "stop": "Stoppen", "restart": "Neustarten",
	"kill": "Beenden (SIGKILL)", "running": "Läuft", "offline": "Offline",
	"crashed": "Abgestürzt", "terminal": "Konsole", "files": "Dateien",
	"backups": "Backups", "schedules": "Zeitpläne", "config": "Konfiguration",
	"stats": "Statistiken", "players": "Spieler", "webhooks": "Webhooks",
	"NOT_AUTHORIZED":     "Sie sind nicht berechtigt, auf diese Ressource zuzugreifen.",
	"INVALID_JSON":       "Der Anfragekörper ist kein gültiges JSON.",
	"TRAVERSAL DETECTED": "Pfad-Traversal erkannt. Dieser Vorfall wurde gemeldet.",
	"INVALID_TOKEN":      "Ihr Sitzungstoken ist ungültig oder abgelaufen.",
	"BAD_PASSWORD":       "Falscher Benutzername oder falsches Passwort.",
	"ACCOUNT_DISABLED":   "Dieses Konto ist deaktiviert.",
	"MFA_REQUIRED":       "Multi-Faktor-Authentifizierungscode erforderlich.",
	"INVALID_MFA":        "Ungültiger Multi-Faktor-Authentifizierungscode.",
	"ALREADY_EXISTS":     "Ein Datensatz mit diesem Namen existiert bereits.",
}

var Russian = map[string]string{
	"login": "Вход", "logout": "Выход", "username": "Имя пользователя",
	"password": "Пароль", "dashboard": "Панель", "servers": "Серверы",
	"users": "Пользователи", "roles": "Роли", "settings": "Настройки",
	"save": "Сохранить", "cancel": "Отмена", "delete": "Удалить", "edit": "Изменить",
	"create": "Создать", "start": "Запустить", "stop": "Остановить", "restart": "Перезапустить",
	"kill": "Принудительно завершить", "running": "Работает", "offline": "Выключен",
	"crashed": "Аварийно завершился", "terminal": "Консоль", "files": "Файлы",
	"backups": "Резервные копии", "schedules": "Расписания", "config": "Конфигурация",
	"stats": "Статистика", "players": "Игроки", "webhooks": "Вебхуки",
	"NOT_AUTHORIZED":     "У вас нет прав доступа к этому ресурсу.",
	"INVALID_JSON":       "Тело запроса не является корректным JSON.",
	"TRAVERSAL DETECTED": "Обнаружен обход пути. Инцидент зарегистрирован.",
	"INVALID_TOKEN":      "Ваш токен сессии недействителен или истёк.",
	"BAD_PASSWORD":       "Неверное имя пользователя или пароль.",
	"ACCOUNT_DISABLED":   "Эта учётная запись отключена.",
	"MFA_REQUIRED":       "Требуется код двухфакторной аутентификации.",
	"INVALID_MFA":        "Неверный код двухфакторной аутентификации.",
	"ALREADY_EXISTS":     "Запись с таким именем уже существует.",
}

var Spanish = map[string]string{
	"login": "Iniciar sesión", "logout": "Cerrar sesión", "username": "Usuario",
	"password": "Contraseña", "dashboard": "Panel", "servers": "Servidores",
	"users": "Usuarios", "roles": "Roles", "settings": "Ajustes",
	"save": "Guardar", "cancel": "Cancelar", "delete": "Eliminar", "edit": "Editar",
	"create": "Crear", "start": "Iniciar", "stop": "Detener", "restart": "Reiniciar",
	"kill": "Forzar cierre", "running": "En ejecución", "offline": "Apagado",
	"crashed": "Falló", "terminal": "Consola", "files": "Archivos",
	"backups": "Copias de seguridad", "schedules": "Programaciones", "config": "Configuración",
	"stats": "Estadísticas", "players": "Jugadores", "webhooks": "Webhooks",
	"NOT_AUTHORIZED":     "No está autorizado a acceder a este recurso.",
	"INVALID_JSON":       "El cuerpo de la solicitud no es JSON válido.",
	"TRAVERSAL DETECTED": "Se detectó un recorrido de ruta. El incidente fue reportado.",
	"INVALID_TOKEN":      "Su token de sesión no es válido o ha caducado.",
	"BAD_PASSWORD":       "Usuario o contraseña incorrectos.",
	"ACCOUNT_DISABLED":   "Esta cuenta está deshabilitada.",
	"MFA_REQUIRED":       "Se requiere el código de autenticación de dos factores.",
	"INVALID_MFA":        "Código de autenticación de dos factores no válido.",
	"ALREADY_EXISTS":     "Ya existe un registro con este nombre.",
}

var French = map[string]string{
	"login": "Connexion", "logout": "Déconnexion", "username": "Nom d'utilisateur",
	"password": "Mot de passe", "dashboard": "Tableau de bord", "servers": "Serveurs",
	"users": "Utilisateurs", "roles": "Rôles", "settings": "Paramètres",
	"save": "Enregistrer", "cancel": "Annuler", "delete": "Supprimer", "edit": "Modifier",
	"create": "Créer", "start": "Démarrer", "stop": "Arrêter", "restart": "Redémarrer",
	"kill": "Forcer l'arrêt", "running": "En cours", "offline": "Hors ligne",
	"crashed": "Planté", "terminal": "Console", "files": "Fichiers",
	"backups": "Sauvegardes", "schedules": "Planifications", "config": "Configuration",
	"stats": "Statistiques", "players": "Joueurs", "webhooks": "Webhooks",
	"NOT_AUTHORIZED":     "Vous n'êtes pas autorisé à accéder à cette ressource.",
	"INVALID_JSON":       "Le corps de la requête n'est pas un JSON valide.",
	"TRAVERSAL DETECTED": "Traversée de chemin détectée. L'incident a été signalé.",
	"INVALID_TOKEN":      "Votre jeton de session est invalide ou expiré.",
	"BAD_PASSWORD":       "Nom d'utilisateur ou mot de passe incorrect.",
	"ACCOUNT_DISABLED":   "Ce compte est désactivé.",
	"MFA_REQUIRED":       "Code d'authentification à deux facteurs requis.",
	"INVALID_MFA":        "Code d'authentification à deux facteurs invalide.",
	"ALREADY_EXISTS":     "Un enregistrement portant ce nom existe déjà.",
}

func init() {
	Register("de", German)
	Register("ru", Russian)
	Register("es", Spanish)
	Register("fr", French)
}
