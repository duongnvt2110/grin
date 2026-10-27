$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class GrinWindowTitles {
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("kernel32.dll", SetLastError=true)] public static extern void SetLastError(uint code);
  [DllImport("user32.dll", CharSet=CharSet.Unicode, SetLastError=true)] public static extern int GetWindowTextLength(IntPtr hWnd);
  [DllImport("user32.dll", CharSet=CharSet.Unicode, SetLastError=true)] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int maxCount);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
}
'@
$foreground = [GrinWindowTitles]::GetForegroundWindow()
$uuidTitle = '[0-9a-f]{8}(?:-[0-9a-f]{1,4}){0,4}'
$terminals = [System.Collections.Generic.List[object]]::new()
$script:maxCandidates = 256
$script:maxSurfaces = 4096
$script:scanned = 0
$script:complete = $true
$script:stopped = $false
$callback = [GrinWindowTitles+EnumWindowsProc]{
  param($hWnd, $lParam)
  $script:scanned++
  if ($script:scanned -gt $maxSurfaces) { $script:complete = $false; $script:stopped = $true; return $false }
  if (-not [GrinWindowTitles]::IsWindowVisible($hWnd)) { return $true }
  # A zero-length caption is valid; an API error is not an exhaustive read.
  [GrinWindowTitles]::SetLastError(0)
  $length = [GrinWindowTitles]::GetWindowTextLength($hWnd)
  $lengthError = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
  if ($length -le 0) {
    if ($lengthError -ne 0) { $script:complete = $false }
    return $true
  }
  $title = [System.Text.StringBuilder]::new($length + 1)
  $copied = [GrinWindowTitles]::GetWindowText($hWnd, $title, $title.Capacity)
  if ($copied -le 0 -or $copied -lt $length) {
    $script:complete = $false
    return $true
  }
  # The title can grow between sizing and reading. Recheck before accepting
  # this window; a truncated title must not authorize a complete scan.
  [GrinWindowTitles]::SetLastError(0)
  $lengthAfter = [GrinWindowTitles]::GetWindowTextLength($hWnd)
  $lengthAfterError = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
  if ($lengthAfterError -ne 0 -or $lengthAfter -ne $copied) {
    $script:complete = $false
    return $true
  }
  $name = $title.ToString()
  if ($name -match $uuidTitle) {
    if ($terminals.Count -ge $script:maxCandidates) { $script:complete = $false; $script:stopped = $true; return $false }
    $terminals.Add([pscustomobject]@{
      api = 'win32-window-title'
      terminal_id = ('0x{0:X}' -f $hWnd.ToInt64())
      title = $name
      foreground = ($hWnd -eq $foreground)
      focused = $false
    })
  }
  return $true
}
$enumerated = [GrinWindowTitles]::EnumWindows($callback, [IntPtr]::Zero)
if (-not $enumerated -and -not $script:stopped) { $script:complete = $false }
ConvertTo-Json -InputObject @{
  supported = $true
  complete = ($script:complete -and $enumerated -and -not $script:stopped)
  sources = @('win32-window-title')
  terminals = @($terminals.ToArray())
} -Depth 4 -Compress
