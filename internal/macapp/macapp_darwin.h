// Called from Go on any thread; each hands its work to the main thread.
void rrRun(const void *icon, int iconLength);
void rrOpenWindow(int id, const char *title, const char *url, const char *frameName, int devtools);
void rrFocusWindow(int id);
void rrCloseWindow(int id);
void rrSetRecent(const char *titles); // newline-separated
void rrShowError(const char *message);
void rrTerminate(void);
