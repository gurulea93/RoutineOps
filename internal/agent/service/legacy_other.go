//go:build !windows

package service

import "log/slog"

// RemoveLegacyArtifacts — no-op вне Windows: ручная распаковка MSI в C:\mdm-extract
// и параллельные каталоги установки — проблема только Windows. На macOS/Linux
// раскладку службы делает relocateForService в стабильные пути.
func RemoveLegacyArtifacts(_ *slog.Logger) {}
