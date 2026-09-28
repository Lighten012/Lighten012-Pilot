package main

import (
	"embed"

	"github.com/Lighten012/Lighten012-Pilot/internal/pilot"
)

//go:embed web/*
var assets embed.FS

func main() { pilot.Run(assets) }
