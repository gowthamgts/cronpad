import { CopyBlock } from "@/components/CopyBlock";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

const DOCS = "https://cronpad.com/docs";
const SKILL_CMD = "curl -fsSL https://raw.githubusercontent.com/gowthamgts/cronpad/main/skills/cronpad/SKILL.md -o skills/cronpad/SKILL.md --create-dirs";

const linkClass = "font-medium text-link underline-offset-4 hover:underline";

/** Entry points for driving cronpad from code and coding agents. */
export function IntegrateCard() {
  return (
    <Card>
      <CardHeader>
        <CardTitle tab>integrate with code</CardTitle>
        <CardDescription>Create and manage jobs from your app or agent</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid min-w-0 gap-2">
          <p className="text-xs font-medium text-ink-soft">AI skill · for coding agents</p>
          <CopyBlock text={SKILL_CMD} label="skill install command" wrap />
          <a href={`${DOCS}/ai/agent-skill/`} target="_blank" rel="noreferrer" className={`${linkClass} text-xs`}>How the skill works</a>
        </div>
        <div className="grid min-w-0 gap-1">
          <p className="text-xs font-medium text-ink-soft">SDK</p>
          <p className="text-sm text-ink-soft">
            Official SDKs are not available yet. Use the{" "}
            <a href={`${DOCS}/api/`} target="_blank" rel="noreferrer" className={linkClass}>REST API</a> or the{" "}
            <a href={`${DOCS}/ai/mcp-server/`} target="_blank" rel="noreferrer" className={linkClass}>MCP server</a>.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}
