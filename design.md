# TerminalUI Design System

## Overview

TerminalUI is a dark, monospaced, hacker-green-on-black design system built for CLI-inspired web apps and terminal emulators. It channels the aesthetics of classic terminal interfaces with phosphor green text, sharp corners, and zero ornamentation. Every element is rendered in monospace type for authentic command-line fidelity. The system is unapologetically minimal, using colored borders and text instead of shadows or gradients for all visual hierarchy.

---

## Colors

- **Primary** (#00FF41): Green — commands, active elements, success
- **Secondary** (#FF6600): Amber — warnings, highlights, prompts
- **Tertiary** (#00BFFF): Cyan — links, info, selection accents
- **Background** (#0D0D0D): Terminal background, page base
- **Surface** (#141414): Panel backgrounds, card fills
- **Success** (#00FF41): Exit code 0, passed tests
- **Warning** (#FF6600): Deprecation notices, caution
- **Error** (#FF0040): Exit errors, failed processes
- **Info** (#00BFFF): Informational output, hints

## Typography

All text in TerminalUI uses monospace to maintain terminal authenticity.

- **Headline Font**: JetBrains Mono
- **Body Font**: JetBrains Mono
- **Mono Font**: JetBrains Mono

- **Display**: JetBrains Mono 32px bold, 1.2 line height, 0.01em tracking
- **Headline**: JetBrains Mono 24px bold, 1.25 line height
- **Subhead**: JetBrains Mono 18px bold, 1.3 line height
- **Body Large**: JetBrains Mono 16px regular, 1.6 line height, 0.02em tracking
- **Body**: JetBrains Mono 14px regular, 1.6 line height, 0.02em tracking
- **Body Small**: JetBrains Mono 13px regular, 1.5 line height, 0.02em tracking
- **Caption**: JetBrains Mono 12px regular, 1.4 line height, 0.03em tracking
- **Overline**: JetBrains Mono 11px medium, 1.4 line height, 0.1em tracking
- **Code**: JetBrains Mono 14px regular, 1.6 line height, 0.02em tracking

---

## Spacing

- **Base unit:** 4px
- **Scale:** `4px / 8px / 12px / 16px / 24px / 32px / 48px / 64px`
- **Component padding:** Buttons `8px 16px`, Panels 16px, Inputs `8px 12px`
- **Section spacing:** 32px between sections, 48px for page-level breaks
- **Terminal line height:** 20px fixed for output lines
- **Tab width:** `8ch` (8 characters) for indentation

## Border Radius

- **None** (0px): All standard elements
- **Small** (0px): Identical to None
- **Medium** (0px): Identical to None
- **Large** (0px): Identical to None
- **XL** (0px): Identical to None
- **Full** (0px): Even avatars use sharp squares

## Elevation

TerminalUI uses no shadows. All elevation is communicated through colored borders.
- **Subtle**: None (use 1px #222222). Panels, containers.
- **Medium**: None (use 1px #444444). Active inputs, hover panels.
- **Large**: None (use 1px #00FF41). Focus states, modals.
- **Overlay**: None (use `background: #000000 at 80%). Backdrop behind modals.
- **Scanline**: repeating-linear-gradient(0deg, transparent, transparent 1px, #00FF41 at 3% 1px, #00FF41 at 3% 2px). Optional CRT scanline overlay.

## Components

### Buttons
- **Primary**: Transparent fill, #00FF41 text, 1px #00FF41 border. Hover: #00FF41 bg, #0D0D0D text.
- **Secondary**: Transparent fill, #00BFFF text, 1px #00BFFF border. Hover: #00BFFF bg, #0D0D0D text.
- **Ghost**: Transparent fill, #8B8B8B text, 1px transparent border. Hover: 1px #444444.
- **Destructive**: Transparent fill, #FF0040 text, 1px #FF0040 border. Hover: #FF0040 bg, #0D0D0D text.
- **Sizes**: Small `28px h / 6px 12px pad`, Medium `36px h / 8px 16px pad`, Large `44px h / 10px 24px pad`
- **Disabled**: 30% opacity, disabled cursor, border becomes #333333
- **Text prefix**: Optional `>` or `$` character before label for CLI feel

### Cards
- **Default**: #141414 fill, 1px #222222 border, no shadow. Hover: border-color: #444444.
- **Elevated**: #1A1A1A fill, 1px #444444 border, no shadow. Hover: border-color: #00FF41.
Padding: 16px. Border radius: 0px. Header line uses #00FF41 text with `---` ASCII divider.

### Inputs
- **Default**: 1px #444444 border, #0D0D0D fill, #8B8B8B label color.
- **Hover**: 1px #666666 border, #0D0D0D fill, #8B8B8B label color.
- **Focus**: 1px #00FF41 border, #0D0D0D fill, #00FF41 label color.
- **Error**: 1px #FF0040 border, #0D0D0D fill, #FF0040 label color.
- **Disabled**: 1px #222222 border, #141414 fill, #555555 label color.
** JetBrains Mono 12px/500, positioned above with 4px gap, rendered as `// label` **label, ** JetBrains Mono 11px/400 in #555555, error helper in #FF0040 **helper text, ** `>` prefix inside input, #00FF41 color **prompt symbol, ** `block` style, blinking #00FF41 **cursor.

### Chips
- **Filter**: Transparent fill, #8B8B8B text, 1px #444444 border, square.
- **Status**: Transparent fill, varies text, 1px (matches semantic) border, square.
Active filter chips invert: #00FF41 bg, #0D0D0D text.

### Lists
JetBrains Mono 14px/400, metadata in #555555 text. 32px (compact terminal rows) row height, 1px dashed #222222 or ASCII --- divider, #1A1A1A hover background, #00FF41 text with `>` prefix indicator active/selected, #555555 text, right-aligned, `4ch` wide line numbers.

### Checkboxes
16px square, 0px radius, 1px #444444 border. `[ ]` unchecked, `[x]` checked rendered as ascii, 8px label gap. Checked: #00FF41 `x` character. Disabled: #333333 border, #555555 text.

### Radio Buttons
16px circle equivalent, 1px #444444 border. `( )` unselected, `(*)` selected rendered as ascii, 8px label gap. Selected: #00FF41 `*` character. Disabled: #333333 border, #555555 text.

### Tooltips
#1A1A1A fill, #00FF41, JetBrains Mono 12px/400 text, 1px #444444 border, 0px border radius. `4px/8px` padding, None (box-style, no arrow) arrow, 320px (wider for code content) max width.
---

## Do's and Don'ts

1. **Do** use JetBrains Mono for ALL text — mixing in sans-serif fonts breaks the terminal illusion.
2. **Do** use inverted colors (green bg, black text) for hover and active states.
3. **Do** maintain 0px border radius on every element; sharp corners are non-negotiable.
4. **Don't** introduce box shadows; borders are the only permitted elevation mechanism.
5. **Don't** use background fills on buttons at rest; they should all be transparent with borders.
6. **Don't** add images, icons, or emoji; use ASCII art and text characters exclusively.
7. **Do** include optional scanline and CRT effects for immersive terminal experiences.
8. **Don't** use line heights below 1.5 for body text; readability on dark backgrounds requires air.
9. **Do** prefix interactive elements with CLI symbols like `>`, `$`, or `#` where appropriate.
10. **Don't** animate transitions with easing curves; use instant or stepped transitions only.