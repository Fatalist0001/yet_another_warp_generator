// Package web содержит статические файлы веб-генератора.
//
// Они встраиваются в бинарник через go:embed, поэтому публичному серверу
// (`warpgen web`) не нужен рабочий каталог с исходниками и нельзя случайно
// отдать файл из репозитория по HTTP.
package web

import "embed"

// Assets — index.html, app.js, warpgen.js и локальные шрифты.
//
//go:embed index.html app.js warpgen.js fonts/*
var Assets embed.FS
