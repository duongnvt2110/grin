ObjC.import('AppKit');

const terminals = [];
const sources = [];
const maxCandidates = 256;
const maxSurfaces = 4096;
let scanned = 0;
let complete = true;
let stopped = false;

function value(object, property) {
  try {
    return object[property]();
  } catch (_) {
    return '';
  }
}

// Title reads are required identity evidence. Unlike optional metadata,
// a failed title getter must make this provider's enumeration incomplete.
function requiredTitle(object, property) {
  const result = object[property]();
  return result == null ? '' : String(result);
}

function optionalTitle(object, property) {
  return typeof object[property] === 'function' ? requiredTitle(object, property) : '';
}

function isRunning(bundleID) {
  const apps = $.NSWorkspace.sharedWorkspace.runningApplications;
  for (let index = 0; index < apps.count; index++) {
    if (ObjC.unwrap(apps.objectAtIndex(index).bundleIdentifier) === bundleID) return true;
  }
  return false;
}

function emit(source, id, title, workspace, foreground, focused, selectedTab, windowID, tabID, activeInTab) {
  if (stopped) return;
  scanned++;
  if (scanned > maxSurfaces) {
    complete = false;
    stopped = true;
    return;
  }
  const text = String(title || '');
  if (!/[0-9a-f]{8}/i.test(text)) return;
  if (terminals.length >= maxCandidates) {
    complete = false;
    stopped = true;
    return;
  }
  terminals.push({
    api: source,
    terminal_id: String(id),
    window_id: windowID ? String(windowID) : undefined,
    tab_id: tabID ? String(tabID) : undefined,
    title: text,
    workspace: String(workspace || ''),
    foreground: Boolean(foreground),
    focused: Boolean(focused),
    selected_tab: typeof selectedTab === 'boolean' ? selectedTab : undefined,
    active_in_tab: typeof activeInTab === 'boolean' ? activeInTab : undefined
  });
}

try {
  if (isRunning('com.mitchellh.ghostty')) {
    const app = Application('Ghostty');
    const windows = app.windows();
    sources.push('ghostty');
    const frontmost = Boolean(value(app, 'frontmost'));
    const frontWindowID = frontmost ? value(app, 'frontWindow').id() : null;
    for (const window of windows) {
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
          emit('ghostty-applescript', terminalID, requiredTitle(terminal, 'name'),
            value(terminal, 'workingDirectory'), foreground,
            foreground && selected && activeInTab === true, selected,
            windowID, tabID, activeInTab);
          if (stopped) break;
        }
        if (stopped) break;
      }
      if (stopped) break;
    }
  }
} catch (_) {
  sources.push('ghostty-unavailable');
  complete = false;
}

try {
  if (!stopped && isRunning('com.apple.Terminal')) {
    const app = Application('Terminal');
    const windows = app.windows();
    sources.push('terminal-app');
    const frontmost = Boolean(value(app, 'frontmost'));
    for (const window of windows) {
      const windowID = String(value(window, 'id'));
      const foreground = frontmost && Boolean(value(window, 'frontmost'));
      for (const tab of window.tabs()) {
        const selected = Boolean(value(tab, 'selected'));
        const tty = String(value(tab, 'tty'));
        const customTitle = optionalTitle(tab, 'customTitle');
        const title = customTitle || (selected ? requiredTitle(window, 'name') : '');
        emit('terminal-app-applescript', `window:${windowID}/tty:${tty}`,
          title, '', foreground,
          foreground && selected, selected);
        if (stopped) break;
      }
      if (stopped) break;
    }
  }
} catch (_) {
  sources.push('terminal-app-unavailable');
  complete = false;
}

try {
  if (!stopped && isRunning('com.googlecode.iterm2')) {
    const app = Application('iTerm');
    const windows = app.windows();
    sources.push('iterm2');
    const frontmost = Boolean(value(app, 'frontmost'));
    for (const window of windows) {
      const windowID = String(value(window, 'id'));
      const foreground = frontmost && Boolean(value(window, 'frontmost'));
      for (const tab of window.tabs()) {
        const selectedSession = value(tab, 'currentSession');
        const selectedID = selectedSession ? String(value(selectedSession, 'id')) : '';
        const tabTitle = requiredTitle(tab, 'title');
        for (const session of tab.sessions()) {
          const sessionID = String(value(session, 'id'));
          emit('iterm2-applescript', `window:${windowID}/session:${sessionID}`,
            tabTitle, '', foreground, foreground && sessionID === selectedID);
          if (stopped) break;
        }
        if (stopped) break;
      }
      if (stopped) break;
    }
  }
} catch (_) {
  sources.push('iterm2-unavailable');
  complete = false;
}

const supported = sources.some(source => !source.endsWith('-unavailable'));
JSON.stringify({ supported: supported, complete: complete && supported,
  reason: sources.length ? sources.join(', ') : 'no supported terminal app is running',
  terminals: terminals, sources: sources });
