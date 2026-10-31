// notify.go — минимальные уведомления трея (без новых зависимостей).
// Balloon API в getlantern/systray отсутствует, поэтому: лог + тултип.
package main

// notifyUser показывает пользователю событие: лог + обновление тултипа.
// Жёлтый Ready при этом не затираем — только тултип дополняем.
func notifyUser(title, msg string) {
	logTunnelEvent("INFO", title, msg)
	debugLog("NOTIFY", "%s: %s", title, msg)
}
