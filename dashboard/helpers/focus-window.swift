#!/usr/bin/env swift
// focus-window — Focuses a single window of an app without bringing all windows forward.
// Uses private SkyLight framework APIs (same approach as alt-tab-macos).
//
// Usage: focus-window <pid> <windowIndex>
//   pid: Process ID of the target app
//   windowIndex: 0-based index of the window to focus (0 = frontmost window of that app)

import Cocoa

// Private SkyLight framework APIs
@_silgen_name("GetProcessForPID") @discardableResult
func GetProcessForPID(_ pid: pid_t, _ psn: UnsafeMutablePointer<ProcessSerialNumber>) -> OSStatus

@_silgen_name("_SLPSSetFrontProcessWithOptions") @discardableResult
func _SLPSSetFrontProcessWithOptions(
    _ psn: UnsafeMutablePointer<ProcessSerialNumber>,
    _ wid: CGWindowID,
    _ mode: UInt32
) -> CGError

@_silgen_name("SLPSPostEventRecordTo") @discardableResult
func SLPSPostEventRecordTo(
    _ psn: UnsafeMutablePointer<ProcessSerialNumber>,
    _ bytes: UnsafeMutablePointer<UInt8>
) -> CGError

@_silgen_name("_AXUIElementGetWindow") @discardableResult
func _AXUIElementGetWindow(_ axUiElement: AXUIElement, _ wid: UnsafeMutablePointer<CGWindowID>) -> AXError

func makeKeyWindow(_ psn: inout ProcessSerialNumber, _ wid: CGWindowID) {
    var bytes = [UInt8](repeating: 0, count: 0xf8)
    bytes[0x04] = 0xf8
    bytes[0x3a] = 0x10
    var widCopy = wid
    memcpy(&bytes[0x3c], &widCopy, MemoryLayout<UInt32>.size)
    memset(&bytes[0x20], 0xff, 0x10)
    bytes[0x08] = 0x01
    SLPSPostEventRecordTo(&psn, &bytes)
    bytes[0x08] = 0x02
    SLPSPostEventRecordTo(&psn, &bytes)
}

guard CommandLine.arguments.count >= 3,
      let pid = pid_t(CommandLine.arguments[1]),
      let windowIndex = Int(CommandLine.arguments[2]) else {
    fputs("Usage: focus-window <pid> <windowIndex>\n", stderr)
    exit(1)
}

// Get the AXUIElement for the app and its windows
let appElement = AXUIElementCreateApplication(pid)
var windowsRef: CFTypeRef?
let err = AXUIElementCopyAttributeValue(appElement, kAXWindowsAttribute as CFString, &windowsRef)
guard err == .success, let windows = windowsRef as? [AXUIElement], windowIndex < windows.count else {
    fputs("Failed to get windows (err=\(err.rawValue), index=\(windowIndex))\n", stderr)
    exit(1)
}

let targetWindow = windows[windowIndex]

// Get CGWindowID from AXUIElement
var cgWindowId: CGWindowID = 0
let axErr = _AXUIElementGetWindow(targetWindow, &cgWindowId)
guard axErr == .success, cgWindowId != 0 else {
    fputs("Failed to get CGWindowID (err=\(axErr.rawValue))\n", stderr)
    exit(1)
}

// Focus just this one window
var psn = ProcessSerialNumber()
GetProcessForPID(pid, &psn)
_SLPSSetFrontProcessWithOptions(&psn, cgWindowId, 0x200)
makeKeyWindow(&psn, cgWindowId)

// Also raise via AX for good measure
AXUIElementPerformAction(targetWindow, kAXRaiseAction as CFString)
