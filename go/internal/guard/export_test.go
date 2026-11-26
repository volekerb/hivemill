package guard

// resetHookState drops the process state one HookMain call leaves behind, so the next in-process call starts as a
// fresh worker-guard process would: the cached guard export, the payload's session id and the --py-hook path (with
// the host state cached from it).
func resetHookState() {
	cachedExport.done = false
	cachedExport.path, cachedExport.exp, cachedExport.err = "", nil, nil
	SessionID = ""
	SetPyHook("")
}
