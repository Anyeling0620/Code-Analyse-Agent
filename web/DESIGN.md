---
name: Repo Agent
description: A live reference manual for reading code repositories.
colors:
  deep-ink-case: "#1b2024"
  index-board: "#242b30"
  map-paper: "#f4f1e6"
  paper-deep: "#eae5d6"
  milk-acetate: "#fbf8ee"
  graphite-ink: "#1b2024"
  soft-ink: "#54584f"
  faint-ink: "#63675c"
  oxide-divider: "#a8461f"
  chrome-index: "#e0b11f"
  grass-signal: "#4f6d33"
  teal-annotation: "#2a6e6a"
  ultramarine-instruction: "#3d5c9e"
  violet-profile: "#6a579c"
  sienna-archive: "#96583c"
  vermilion-errata: "#c93a2b"
  primary-action: "#b24a22"
typography:
  display:
    fontFamily: "Archivo, PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif"
    fontSize: "clamp(38px, 4.2vw, 64px)"
    fontWeight: 750
    lineHeight: 0.98
    letterSpacing: "-0.05em"
  headline:
    fontFamily: "Archivo, PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif"
    fontSize: "clamp(26px, 2.6vw, 36px)"
    fontWeight: 750
    lineHeight: 1.1
    letterSpacing: "-0.035em"
  title:
    fontFamily: "Archivo, PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif"
    fontSize: "15px"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Source Serif 4, Songti SC, Noto Serif CJK SC, Source Han Serif SC, serif"
    fontSize: "16.5px"
    fontWeight: 400
    lineHeight: 1.75
  ui:
    fontFamily: "Archivo, PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "Azeret Mono, JetBrains Mono, SFMono-Regular, Consolas, monospace"
    fontSize: "9.5px"
    fontWeight: 500
    lineHeight: 1.2
    letterSpacing: "0.05em"
rounded:
  hairline: "2px"
  punch: "50%"
spacing:
  xs: "4px"
  sm: "8px"
  md: "14px"
  lg: "22px"
  xl: "36px"
  xxl: "58px"
components:
  button-primary:
    backgroundColor: "{colors.primary-action}"
    textColor: "{colors.milk-acetate}"
    rounded: "{rounded.hairline}"
    padding: "10px 16px"
  button-primary-hover:
    backgroundColor: "#9f4520"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.graphite-ink}"
    rounded: "{rounded.hairline}"
    padding: "10px 16px"
  input:
    backgroundColor: "{colors.map-paper}"
    textColor: "{colors.graphite-ink}"
    rounded: "{rounded.hairline}"
    padding: "9px 10px"
  message-leaf:
    backgroundColor: "{colors.milk-acetate}"
    textColor: "{colors.graphite-ink}"
    rounded: "{rounded.hairline}"
    padding: "26px 28px 24px 38px"
  user-slip:
    backgroundColor: "{colors.ultramarine-instruction}"
    textColor: "{colors.milk-acetate}"
    rounded: "{rounded.hairline}"
    padding: "14px 18px"
  tab:
    backgroundColor: "{colors.oxide-divider}"
    textColor: "{colors.milk-acetate}"
    rounded: "{rounded.hairline}"
    padding: "0 8px"
---

# Design System: Repo Agent

## Overview

**Creative North Star: "The Live Reference Manual"**

Repo Agent is a working reference book that happens to be software. The session index is a dark case; the conversation is a paper field; each assistant answer is a milk-acetate leaf punched and tabbed with its section colour. The world is present in the type, the palette, the density, and one signature move — the tabbed leaf and the fore-edge rail — while the navigation, buttons, inputs, and menus stay standard web controls an engineer already knows.

The system is professional, restrained, and trustworthy. It refuses the generic white chat shell, the neon dashboard, and glass decoration. Colour is not scattered as accent: the seven section hues are full-strength boards used for tabs, the user's instruction slip, status stamps, and small reading markers. Long-form prose always sits on paper or acetate, never on a saturated field.

**Key Characteristics:**
- Deep-ink index case and a paper reading field.
- Milk-acetate assistant leaves with punched holes and section tabs.
- A stepped fore-edge rail that tracks message order and reading position.
- Three self-hosted voices: Archivo for UI and headings, Source Serif 4 for report prose, Azeret Mono for machine data.
- Standard controls, precise rules, and steps(2) motion; no easing curves.

## Colors

The palette is a boxed manual's colour wheel: a dark case, a paper field, and seven full-strength divider hues. Hues are assigned by position in the conversation, so the same message keeps its tab colour in the rail, the leaf, and the stage bar.

### Primary
- **Oxide Divider** (#a8461f): new-analysis button, primary tabs, key emphasis. Used at full strength, never as a wash behind prose.
- **Primary Action** (#b24a22): primary buttons and the send control; a half-step deeper than oxide for text contrast.

### Secondary
- **Chrome Index** (#e0b11f): export affordance, index numbers, and the third bar of the favicon. A small area used for action and annotation.
- **Teal Annotation** (#2a6e6a): links, assistant role stamps, and the login card's ACCESS tab.
- **Ultramarine Instruction** (#3d5c9e): the solid user-instruction slip and the first section hue.
- **Violet Profile** (#6a579c): profile and onboarding boards.
- **Sienna Archive** (#96583c): the seventh section hue and archive-like metadata.
- **Grass Signal** (#4f6d33): healthy/online state and completed tool calls.

### Tertiary
- **Vermilion Errata** (#c93a2b): reserved for errors, destructive actions, the approval tab, and risk stamps. Never decorative.

### Neutral
- **Deep Ink Case** (#1b2024): the application case, sidebar, and code-block ground.
- **Index Board** (#242b30): raised dark surfaces inside the case (account card, actions).
- **Map Paper** (#f4f1e6): the main reading field.
- **Paper Deep** (#eae5d6): table headers, inline-code and quote grounds.
- **Milk Acetate** (#fbf8ee): assistant leaves, composer strip, modal surfaces.
- **Graphite Ink** (#1b2024): primary text on paper.
- **Soft Ink** (#54584f): secondary prose, table cells, rules of hierarchy.
- **Faint Ink** (#63675c): placeholders, counters, and the quietest metadata. It still passes body contrast on paper.

### Named Rules
**The One Board Rule.** Saturated section hues belong to tabs, the user slip, status stamps, and small boards. Long-form reading fields are always paper or acetate.
**The Errata Rule.** Vermilion is for errors, destructive actions, and approval risk. It never becomes an accent colour.

## Typography

**Display Font:** Archivo (with PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif)
**Body Font:** Source Serif 4 (with Songti SC, Noto Serif CJK SC, Source Han Serif SC, serif)
**Label/Mono Font:** Azeret Mono (with JetBrains Mono, SFMono-Regular, Consolas, monospace)

**Character:** Archivo gives the manual its tight, sign-painted headings; Source Serif 4 gives reports a reading surface that holds up over long sessions; Azeret Mono speaks for tool names, IDs, metrics, and code. All three are self-hosted WOFF2; no external font stylesheet is loaded.

### Hierarchy
- **Display** (750, clamp(38px, 4.2vw, 64px), 0.98 line-height, -0.05em tracking): the login cover's promise only.
- **Headline** (750, clamp(26px, 2.6vw, 36px), 1.1, -0.035em): empty-state and modal headlines.
- **Title** (700, 15px, 1.25, -0.01em): stage bar, card headings, section titles.
- **Body** (400, 16.5px, 1.75, max ~70ch): report prose, lists, quotes. UI copy uses Archivo at 13–15px.
- **Label** (500, 9.5px, 0.05em, uppercase): role stamps, session metadata, tool names, metrics, and IDs.

### Named Rules
**The Reading Measure Rule.** Report prose stays near 70ch and never stretches across the full desktop width. A wide window earns margin, not longer lines.
**The Machine Voice Rule.** Monospace is for code, data, measurement, names, and IDs — never for long prose or decorative "technical" labels.

## Layout

The shell is a two-column working surface: a 288px deep-ink index board and a flexible paper stage. Below 1180px the index board narrows to 256px; below 900px it becomes a fixed drawer with a scrim and a mobile menu button. The stage is a header row plus a scrolling reading field with an absolutely positioned composer; the composer is cleared by 230px of scroll padding.

The reading column is `min(1000px, calc(100% - 96px))`; below 1180px it is `calc(100% - 64px)`; below 900px it is `calc(100% - 24px)`. The composer uses the same measure. The login page is a 1.15fr / 0.85fr split with a 420px minimum form panel; it stacks below 900px. The share page uses a dark auto / 1fr / auto header over a paper reading field.

Spacing is tight and consistent: 4/8/14/22/36/58px. A heading always has more space above it than below. Tables, code blocks, and figures sit on the same left edge as body text.

## Elevation & Depth

Depth is printed, not floating. The page is flat paper; leaves, cards, and the composer carry a short soft shadow and a 1px cut edge, as if a sheet is lying a millimetre above the page. There is no glow, no layered glass, and no decorative blur. The single backdrop blur is the modal focus scrim, where it does the job of separating a protected decision from the page.

### Shadow Vocabulary
- **Leaf** (`0 1px 2px rgba(20, 24, 20, 0.14), 0 18px 38px -26px rgba(20, 24, 20, 0.5)`): assistant leaves, empty sheet, skeleton leaves.
- **Lift** (`0 2px 4px rgba(20, 24, 20, 0.16), 0 24px 48px -24px rgba(20, 24, 20, 0.55)`): composer, login card, menus, modals.

### Named Rules
**The Printed Edge Rule.** Depth comes from a 1px rule, a short soft shadow, and the material of the sheet — not from a floating card with a large radius.
**The No Glow Rule.** No coloured halos, neon edges, or ambient glows. Status colour sits in a dot or a stamp, not in light.

## Shapes

The form language is a printed manual: square-ish sheets with a 2px hairline radius, 1px rules, stepped tabs, and punched 9px holes. Circles appear only where a physical punch or a status dot requires them. There are no large rounded chat bubbles, no pill-shaped cards, and no capsule buttons: controls are rectangular with a small radius, and tabs are topped with a 2px-radius board.

Tabs are the recurring silhouette: a coloured rectangle seated on a sheet's top edge, a stepped rail down the fore edge, and a small section marker at the head of an answer. Borders are hairlines at 16% ink or 32% ink, never thick side stripes.

## Components

### Buttons
- **Shape:** 2px hairline radius, 10px 16px padding, 13px Archivo 600.
- **Primary:** oxide-orange `#b24a22` on milk text; hover deepens to `#9f4520`; disabled becomes paper-deep with faint ink.
- **Ghost:** transparent with a 32%-ink rule; hover fills paper-deep.
- **Danger:** vermilion `#c93a2b` on milk text; used only for approval and destructive confirmation.
- **Icon buttons:** 34px square, 1px rule, paper-soft surface; the send button uses the primary action; the stop button uses vermilion.

### Chips
- **Style:** 1px rule, transparent or paper-soft ground, 9.5px Azeret Mono, small radius. Used for tool names, role stamps, trace entries, session counts, and statuses.
- **State:** selected/active chips use a full-strength section hue or the ink ground; normal chips stay quiet.

### Cards / Containers
- **Corner Style:** 2px hairline radius; punch holes are 9px circles.
- **Background:** assistant leaves use milk acetate over the paper field; user instructions use solid ultramarine; empty sheets and modals use paper-soft.
- **Shadow Strategy:** Leaf and Lift tokens; no nesting of decorative cards. Tool cards are functional disclosures inside a leaf, not another card layer.
- **Border:** 1px rule; the active session row uses paper as its full board.
- **Internal Padding:** 26px 28px 24px 38px for assistant leaves; 14px 18px for user slips.

### Inputs / Fields
- **Style:** paper ground, 1px 32%-ink border, 2px radius, 9px 10px padding, 13px Archivo.
- **Focus:** teal border plus a 2px teal-tinted ring; the composer uses a paper-deep shadow and a teal hairline focus ring.
- **Error / Disabled:** vermilion rule and 8% wash for errors; disabled controls drop to paper-deep and faint ink.
- **Placeholder:** faint ink at body contrast; no light-gray placeholders.

### Navigation
- **Session index:** dark case, 288px wide; brand block, a full-strength oxide "new analysis" action, the session list, and a footer with profile, account, health, and logout. The active session becomes a paper row with its session hue as a swatch.
- **Mobile:** the index becomes a fixed 320px drawer with a scrim; the stage bar gains a menu button and keeps the section tab and title. The drawer's actions close it before opening a modal.
- **Stage bar:** session title, section tab, health, session/trace IDs, and a READY/RUNNING stamp; no marketing nav.
- **Fore-edge rail:** the signature navigation aid; one tab per message, height proportional to message extent, active tab extended, exportable messages marked with a chrome square.

### Signature Components
**Assistant acetate leaf.** A milk-acetate sheet with two punched holes in the left gutter, a 1px cut edge, a leaf shadow, and a coloured section tab on the top edge. The tab's hue matches the message's position and the stage bar.

**Tool evidence card.** A quiet paper disclosure inside the leaf: mono tool name, calling/done stamp, ink-ground arguments, paper result. Opening it lifts the content with a 90ms two-frame step.

**Acetate composer.** A milk-acetate strip with punch holes on its top edge, a hard-edged shadow, a small "指令" tab, standard textarea, and a primary send square. Metrics and cache figures sit in mono at the foot.

**Login cover.** A dark case with a large Archivo wordmark, a two-line promise, and a stepped index of seven full-strength capability tabs. The form is a paper-soft card with an ACCESS tab; the guest entry is a standard secondary button.

## Do's and Don'ts

### Do:
- **Do** keep report prose on Source Serif 4 at 16.5px/1.75 with a ~70ch measure.
- **Do** use full-strength section hues on tabs, stamps, the user slip, and small boards only.
- **Do** keep the fore-edge rail aligned to message order and message extent, and let the active tab extend.
- **Do** use the three self-hosted faces and the tab/leaf material as the signature.
- **Do** keep standard web controls, visible focus rings, and `prefers-reduced-motion` support.
- **Do** use SVG icons in one 1.75px stroke and label every icon-only button.

### Don't:
- **Don't** introduce rounded chat bubbles, gradient text, glass panels, neon glows, or emoji icons.
- **Don't** put long-form text on a saturated hue or use a saturated hue as a page wash.
- **Don't** use vermilion as an accent; it is reserved for errors, destructive actions, and approval risk.
- **Don't** replace standard navigation, inputs, or menus with manual/costume chrome.
- **Don't** bring back the old green/white ChatGPT palette or the generic white-shell composition.
- **Don't** add large radii, pill cards, or floating glass surfaces; depth is a printed edge and a short soft shadow.
