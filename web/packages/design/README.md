# @cronpad/design

The cronpad design system shared by the dashboard (`web/` in this repo) and the public site: colour tokens
(light and dark), the Maple Mono font files, base styles and the component classes (`.box`, `.box-title`,
`.btn`, `.field`, `.link`, `.eyebrow`, `.marker`, `.graph`, `.live-dot`). Requires Tailwind CSS v4.

```css
@import "tailwindcss";
@import "@cronpad/design";
/* app-specific extensions below */
```

- In this repo, depend on it with `"@cronpad/design": "workspace:*"`.
- From another repo, depend on a pinned commit:
  `"@cronpad/design": "github:gowthamgts/cronpad#<commit>&path:web/packages/design"`.

Change the design here, not in the apps; apps only add their own extensions on top. Maple Mono is licensed
under the SIL Open Font License (`fonts/OFL.txt`).
