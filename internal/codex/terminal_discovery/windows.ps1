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
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowTextLength(IntPtr hWnd);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int maxCount);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
}
'@
$foreground = [GrinWindowTitles]::GetForegroundWindow()
$uuidTitle = '^[0-9a-f]{8}(?:-[0-9a-f]{1,4}){0,4}'
$terminals = [System.Collections.Generic.List[object]]::new()
$callback = [GrinWindowTitles+EnumWindowsProc]{
  param($hWnd, $lParam)
  if (-not [GrinWindowTitles]::IsWindowVisible($hWnd)) { return $true }
  $length = [GrinWindowTitles]::GetWindowTextLength($hWnd)
  if ($length -le 0) { return $true }
  $title = [System.Text.StringBuilder]::new($length + 1)
  [void][GrinWindowTitles]::GetWindowText($hWnd, $title, $title.Capacity)
  $name = $title.ToString()
  if ($name -match $uuidTitle -and $terminals.Count -lt 256) {
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
[void][GrinWindowTitles]::EnumWindows($callback, [IntPtr]::Zero)
ConvertTo-Json -InputObject @{
  supported = $true
  sources = @('win32-window-title')
  terminals = @($terminals.ToArray())
} -Depth 4 -Compress
