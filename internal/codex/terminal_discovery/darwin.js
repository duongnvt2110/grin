ObjC.import('AppKit');

function value(object, property) {
  try {
    return object[property]();
  } catch (_) {
    return '';
  }
}

function isRunning(bundleID) {
  const apps = $.NSWorkspace.sharedWorkspace.runningApplications;
  for (let index = 0; index < apps.count; index++) {
    if (ObjC.unwrap(apps.objectAtIndex(index).bundleIdentifier) === bundleID) return true;
  }
  return false;
}

function emit(source, id, title, workspace, foreground, focused, selectedTab, windowID, tabID, activeInTab) {
  if (!title || !/^[0-9a-f]{8}(?:-[0-9a-f]{1,4}){0,4}/i.test(title)) return;
  terminals.push({
    api: source,
    terminal_id: String(id),
    window_id: windowID ? String(windowID) : undefined,
    tab_id: tabID ? String(tabID) : undefined,
    title: String(title),
    workspace: String(workspace || ''),
    foreground: Boolean(foreground),
    focused: Boolean(focused),
    selected_tab: typeof selectedTab === 'boolean' ? selectedTab : undefined,
    active_in_tab: typeof activeInTab === 'boolean' ? activeInTab : undefined
  });
}

const terminals = [];
const sources = [];

try {
  if (isRunning('com.mitchellh.ghostty')) {
    const app = Application('Ghostty');
    const frontmost = Boolean(value(app, 'frontmost'));
    const frontWindowID = frontmost ? value(app, 'frontWindow').id() : null;
    sources.push('ghostty');
    for (const window of app.windows()) {
      const windowID = String(value(window, 'id'));
      const foreground = frontmost && windowID === String(frontWindowID);
      for (const tab of window.tabs()) {
        const tabID = String(value(tab, 'id'));
        const selected = Boolean(value(tab, 'selected'));
        const focusedTerminal = value(tab, 'focusedTerminal');
        const focusedID = focusedTerminal ? String(value(focusedTerminal, 'id')) : '';
        for (const terminal of tab.terminals()) {
          const terminalID = String(value(terminal, 'id'));
          const activeInTab = focusedID ? terminalID === focusedID : undefined;
          emit('ghostty-applescript', terminalID, value(terminal, 'name'),
            value(terminal, 'workingDirectory'), foreground,
            foreground && selected && activeInTab === true, selected,
            windowID, tabID, activeInTab);
          if (terminals.length >= 256) break;
        }
        if (terminals.length >= 256) break;
      }
      if (terminals.length >= 256) break;
    }
  }
} catch (_) {
  sources.push('ghostty-unavailable');
}

try {
  if (isRunning('com.apple.Terminal') && terminals.length < 256) {
    const app = Application('Terminal');
    const frontmost = Boolean(value(app, 'frontmost'));
    sources.push('terminal-app');
    for (const window of app.windows()) {
      const windowID = String(value(window, 'id'));
      const foreground = frontmost && Boolean(value(window, 'frontmost'));
      const windowTitle = String(value(window, 'name'));
      for (const tab of window.tabs()) {
        const selected = Boolean(value(tab, 'selected'));
        const tty = String(value(tab, 'tty'));
        const customTitle = String(value(tab, 'customTitle'));
        emit('terminal-app-applescript', `window:${windowID}/tty:${tty}`,
          customTitle || (selected ? windowTitle : ''), '', foreground,
          foreground && selected, selected);
        if (terminals.length >= 256) break;
      }
      if (terminals.length >= 256) break;
    }
  }
} catch (_) {
  sources.push('terminal-app-unavailable');
}

try {
  if (isRunning('com.googlecode.iterm2') && terminals.length < 256) {
    const app = Application('iTerm');
    const frontmost = Boolean(value(app, 'frontmost'));
    sources.push('iterm2');
    for (const window of app.windows()) {
      const windowID = String(value(window, 'id'));
      const foreground = frontmost && Boolean(value(window, 'frontmost'));
      for (const tab of window.tabs()) {
        const selectedSession = value(tab, 'currentSession');
        const selectedID = selectedSession ? String(value(selectedSession, 'id')) : '';
        const tabTitle = String(value(tab, 'title'));
        for (const session of tab.sessions()) {
          const sessionID = String(value(session, 'id'));
          emit('iterm2-applescript', `window:${windowID}/session:${sessionID}`,
            tabTitle, '', foreground, foreground && sessionID === selectedID);
          if (terminals.length >= 256) break;
        }
        if (terminals.length >= 256) break;
      }
      if (terminals.length >= 256) break;
    }
  }
} catch (_) {
  sources.push('iterm2-unavailable');
}

JSON.stringify({ supported: sources.some(source => !source.endsWith('-unavailable')),
  reason: sources.length ? sources.join(', ') : 'no supported terminal app is running',
  terminals: terminals, sources: sources });
