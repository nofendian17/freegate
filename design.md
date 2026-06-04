---
version: alpha
name: Cloudflare Investor Relations
description: A clean, high-contrast corporate system with a vivid orange accent and spacious editorial hierarchy.
colors:
  primary: "#f48120"
  secondary: "#000000"
  tertiary: "#ffffff"
  neutral: "#f5f5f5"
  surface: "#ffffff"
  on-surface: "#000000"
  on-primary: "#ffffff"
  border: "#e6e6e6"
  muted: "#666666"
  accent: "#f48120"
typography:
  headline-display:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 45px
    fontWeight: 600
    lineHeight: 54px
    letterSpacing: 0px
  headline-lg:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 38px
    fontWeight: 600
    lineHeight: 46px
    letterSpacing: -1px
  headline-md:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 33px
    fontWeight: 600
    lineHeight: 40px
    letterSpacing: 0px
  headline-sm:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 28px
    fontWeight: 600
    lineHeight: 40px
    letterSpacing: 0px
  body-lg:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 24px
    fontWeight: 400
    lineHeight: 36px
    letterSpacing: 0px
  body-md:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 18px
    fontWeight: 400
    lineHeight: 28px
    letterSpacing: 0px
  body-sm:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 16px
    fontWeight: 400
    lineHeight: 24px
    letterSpacing: 0px
  label-lg:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 14px
    fontWeight: 400
    lineHeight: 20px
    letterSpacing: 0px
  label-md:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Droid Sans, Helvetica Neue, sans-serif"
    fontSize: 14px
    fontWeight: 300
    lineHeight: 20px
    letterSpacing: 0px
  label-sm:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Fira Sans, Helvetica Neue, sans-serif"
    fontSize: 12px
    fontWeight: 400
    lineHeight: 16px
    letterSpacing: 0px
rounded:
  none: 0px
  sm: 4px
  md: 5px
  lg: 8px
  xl: 12px
  full: 9999px
spacing:
  xs: 10px
  sm: 20px
  md: 30px
  lg: 50px
  xl: 72px
  gutter: 24px
  section: 96px
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
    typography: "{typography.label-lg}"
    rounded: "{rounded.md}"
    padding: 13px 20px
    height: 44px
  button-secondary:
    backgroundColor: "transparent"
    textColor: "{colors.primary}"
    typography: "{typography.label-lg}"
    rounded: "{rounded.sm}"
    padding: 13px 20px
    height: 44px
  button-tertiary:
    backgroundColor: "transparent"
    textColor: "{colors.primary}"
    typography: "{typography.label-md}"
    rounded: "{rounded.none}"
    padding: 0px
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
    rounded: "{rounded.lg}"
    padding: 8px 8px 8px 16px
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
    rounded: "{rounded.sm}"
    padding: 13px 20px
  chip:
    backgroundColor: "{colors.neutral}"
    textColor: "{colors.on-surface}"
    rounded: "{rounded.full}"
    padding: 6px 12px
---

# Cloudflare Investor Relations

## Overview
This interface feels crisp, corporate, and highly legible, with a strong investor-relations tone rather than a consumer-product feel. The composition is spacious and editorial, using a very light background, centered content, and a vivid orange brand accent to signal confidence and energy. It is professional and minimal, with enough visual warmth from the accent color to keep the experience approachable.

## Colors
- **Primary (#f48120):** A bright Cloudflare orange used for the brand mark, active navigation, key metrics, and the sweeping hero graphic. It should be reserved for emphasis and calls to action.
- **Secondary (#000000):** Deep black used for the main headline treatment, body copy, and high-contrast text hierarchy.
- **Tertiary (#ffffff):** Pure white used as the main page canvas and to preserve clarity inside the large orange hero shape.
- **Neutral (#f5f5f5):** A soft off-white supporting tone for subtle surfaces, quiet sectioning, or low-emphasis containers.
- **Surface (#ffffff):** The default panel and page surface color, keeping the layout clean and editorial.
- **On-surface (#000000):** The primary text color on white surfaces; it provides the strong, institutional readability seen in the screenshot.
- **On-primary (#ffffff):** Text and icon color on orange backgrounds when contrast must remain maximized.
- **Border (#e6e6e6):** A restrained divider tone for subtle rules, column separators, and lightweight UI boundaries.
- **Muted (#666666):** A secondary text tone for less prominent labels or supporting copy, used sparingly to maintain the otherwise stark contrast.
- **Accent (#f48120):** A duplicate semantic accent token for flexibility when a design needs the same brand orange in multiple contexts.

## Typography
The system is built on the native Apple/system sans stack: `-apple-system` first, followed by common platform fallbacks. This gives the site a modern, trustworthy, slightly utilitarian voice that suits finance and corporate communications.

Headlines are heavy and restrained, with `headline-display` used for the main page title, `headline-lg` and `headline-md` for section titles, and `headline-sm` for smaller subsection headings like “Corporate Overview.” The hierarchy relies more on weight, size, and spacing than on decorative styling.

Body text is large and comfortable, with `body-lg` reflecting the prominent centered paragraph treatment visible in the screenshot. Labels and navigation use lighter or smaller styles such as `label-lg`, `label-md`, and `label-sm`, staying concise and functional. Letter spacing is essentially neutral; the system avoids visible uppercase tracking or ornamental typographic effects.

## Layout
The layout is centered and wide, with generous whitespace around the main content blocks. Content is organized in stacked sections: top navigation, large hero area, centered overview copy, then a metrics grid.

Spacing follows a clear rhythm based on 10px, 20px, 30px, 50px, and 72px increments, which creates measured breathing room without feeling loose. Section separation should remain generous, and inner groupings should use smaller tokens to keep the page grounded. Cards, metric columns, and navigation elements should align to a simple grid with ample horizontal padding and visually balanced center alignment.

## Elevation & Depth
The design is mostly flat and tonal rather than shadow-driven. Depth is created through contrast, whitespace, and the subtle separation of sections and columns instead of heavy layering.

Where depth is needed, it should be understated: a light shadow on cards is acceptable, but borders and spacing are preferred for structure. The strongest visual hierarchy comes from the orange hero shape against the white field and from bold black typography, not from material-like elevation.

## Shapes
The shape language is minimal and slightly softened. Interactive elements use small radii, with `rounded.md` at 5px for primary buttons and `rounded.sm` at 4px for secondary controls and inputs.

Overall, the system feels architectural and precise rather than rounded or playful. Large graphic curves in the hero area provide the only expressive shape, while UI controls stay disciplined and unobtrusive.

## Components
**Buttons**
- Use `button-primary` for the main action. It should be orange with white text, 13px by 20px padding, and a 44px height for dependable click targets.
- Use `button-secondary` for lower-emphasis actions. It should be transparent with orange text and border, maintaining the same footprint but slightly softer corners.
- Use `button-tertiary` for inline or text-only actions. It should appear as plain orange text with no fill and no border.
- Buttons should remain compact and restrained; avoid oversized pill shapes or loud shadows.

**Cards**
- Use `card` for summary modules and metric containers.
- Cards are white, minimally bordered, and lightly shadowed, with padding biased slightly left (`8px 8px 8px 16px`) to support content-heavy layouts.
- Keep card content simple and information-dense rather than decorative.

**Inputs**
- Inputs should be quiet and functional, matching the button and card radius language.
- Favor white surfaces, thin borders, and concise padding so fields blend into the broader corporate page rather than calling attention to themselves.

**Navigation**
- Top navigation should be text-forward, horizontally aligned, and low ornament.
- Active items use the primary orange, while inactive links stay black or dark gray.
- Keep the search icon minimal and aligned with the same restrained spacing logic.

**Metrics / Stat Blocks**
- Metric blocks should emphasize the number first, using the primary orange at a large size, followed by smaller black supporting text.
- Column separators can use the `border` tone or a very light orange tint to suggest division without creating heaviness.
- These blocks should feel editorial and data-centric, not card-heavy.

**Links**
- Underlined text links are acceptable for tertiary actions and should remain orange.
- Avoid button-like treatment for simple navigational references.

## Do's and Don'ts
- Do keep the page bright, open, and centered with generous whitespace.
- Do use the orange accent sparingly to signal brand and important data.
- Do favor bold black headlines and large, readable body copy.
- Do keep borders, dividers, and shadows subtle; let typography do the work.
- Don't introduce dark backgrounds or heavy decorative gradients outside the brand hero treatment.
- Don't over-round controls; maintain the small, precise radius language.
- Don't use playful colors, dense UI chrome, or loud motion that would weaken the investor-relations tone.
- Don't make secondary elements compete with the headline or key metrics.