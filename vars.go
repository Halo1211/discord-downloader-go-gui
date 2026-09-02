package main

import (
	"os"
)

var (
	projectName     = "discord-downloader-go-gui"
	projectLabel    = "Discord Downloader Go GUI"
	projectRepoBase = "Halo1211/discord-downloader-go-gui"
	projectRepoURL  = "https://github.com/" + projectRepoBase
	projectIcon     = "https://cdn.discordapp.com/icons/780985109608005703/9dc25f1b91e6d92664590254e0797fad.webp?size=256"
	projectVersion  = "1.0.0" // follows Semantic Versioning, (http://semver.org/)

	pathCache             = "cache"
	pathCacheHistory      = pathCache + string(os.PathSeparator) + "history"
	pathCacheSettingsJSON = pathCache + string(os.PathSeparator) + "settings.json"
	pathCacheSettingsYAML = pathCache + string(os.PathSeparator) + "settings.yaml"
	pathCacheDuplo        = pathCache + string(os.PathSeparator) + ".duplo"
	pathCacheTwitter      = pathCache + string(os.PathSeparator) + "twitter.json"
	pathCacheInstagram    = pathCache + string(os.PathSeparator) + "instagram.json"
	pathConstants         = pathCache + string(os.PathSeparator) + "constants.json"
	pathDatabaseBase      = "database"
	pathDatabaseBackups   = "backups"

	defaultReact = builtInDefaultReact
	limitMsg     = 2000
)

const builtInDefaultReact = "✅"
