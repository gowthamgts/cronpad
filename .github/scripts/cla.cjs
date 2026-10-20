'use strict';

const PHRASE = 'I have read the CLA Document and I hereby sign the CLA';
const MARKER = '<!-- cronpad-cla -->';
const BRANCH = 'cla-signatures';
const PATH = 'signatures/cla.json';

function exempt(author, allowlist) {
  return author && (author.type === 'Bot' || author.login.toLowerCase().endsWith('[bot]') || allowlist.has(author.login.toLowerCase()));
}
function missing(error) { return error.status === 404; }
async function readSignatures(github, repo) {
  try {
    const {data} = await github.rest.repos.getContent({...repo, path: PATH, ref: BRANCH});
    return {sha: data.sha, entries: JSON.parse(Buffer.from(data.content, 'base64').toString('utf8'))};
  } catch (error) { if (!missing(error)) throw error; return {entries: []}; }
}
async function ensureBranch(github, repo, defaultBranch) {
  try { await github.rest.git.getRef({...repo, ref: `heads/${BRANCH}`}); }
  catch (error) {
    if (!missing(error)) throw error;
    const {data} = await github.rest.git.getRef({...repo, ref: `heads/${defaultBranch}`});
    try { await github.rest.git.createRef({...repo, ref: `refs/heads/${BRANCH}`, sha: data.object.sha}); }
    catch (conflict) { if (conflict.status !== 422 && conflict.status !== 409) throw conflict; }
  }
}
async function sign(github, repo, context, version, pr) {
  await ensureBranch(github, repo, context.payload.repository.default_branch);
  for (let attempt = 0; attempt < 3; attempt++) {
    const state = await readSignatures(github, repo);
    const user = context.payload.comment.user;
    if (state.entries.some(e => e.id === user.id && e.cla_version === version)) return;
    state.entries.push({id: user.id, login: user.login, cla_version: version,
      signed_at: context.payload.comment.created_at, pull_request: pr.number, comment_url: context.payload.comment.html_url});
    try {
      await github.rest.repos.createOrUpdateFileContents({...repo, branch: BRANCH, path: PATH,
        message: `Record CLA ${version} signature for user ${user.id}`, content: Buffer.from(JSON.stringify(state.entries, null, 2) + '\n').toString('base64'), ...(state.sha ? {sha: state.sha} : {})});
      return;
    } catch (error) { if (![409, 422].includes(error.status) || attempt === 2) throw error; }
  }
}

// Only trusted default-branch code invokes this module; PR files are never executed.
async function run({github, context, version = '1.2', maintainers = ''}) {
  const repo = context.repo;
  const isComment = context.eventName === 'issue_comment';
  const body = (context.payload.comment?.body || '').trim();
  if (isComment && (!context.payload.issue?.pull_request || (!body.includes(PHRASE) && !/\brecheck\b/i.test(body)))) return;
  const number = isComment ? context.payload.issue.number : context.payload.pull_request.number;
  const {data: pr} = await github.rest.pulls.get({...repo, pull_number: number});
  if (pr.state === 'closed') return;
  if (isComment && body === PHRASE) await sign(github, repo, context, version, pr);
  const allowlist = new Set(maintainers.split(',').map(x => x.trim().toLowerCase()).filter(Boolean));
  const commits = await github.paginate(github.rest.pulls.listCommits, {...repo, pull_number: number, per_page: 100});
  const required = new Map();
  const unlinked = new Set();
  for (const commit of commits) {
    if (!commit.author) { unlinked.add(commit.commit.author.email); continue; }
    if (!exempt(commit.author, allowlist)) required.set(commit.author.id, commit.author.login);
  }
  const {entries} = await readSignatures(github, repo);
  const signed = new Set(entries.filter(e => e.cla_version === version).map(e => e.id));
  const unsigned = [...required].filter(([id]) => !signed.has(id)).map(([,login]) => login).sort();
  const ok = unsigned.length === 0 && unlinked.size === 0;
  const claURL = `https://github.com/${repo.owner}/${repo.repo}/blob/${context.payload.repository.default_branch}/CLA.md`;
  const description = ok ? 'All required authors signed the CLA' : `Waiting for CLA: ${[...unsigned.map(a => '@' + a), ...(unlinked.size ? ['unlinked commit email'] : [])].join(', ')}`.slice(0, 140);
  await github.rest.repos.createCommitStatus({...repo, sha: pr.head.sha, state: ok ? 'success' : 'failure', context: 'cla/signed', description, target_url: claURL});
  const comments = await github.paginate(github.rest.issues.listComments, {...repo, issue_number: number, per_page: 100});
  const existing = comments.find(c => c.user?.type === 'Bot' && c.body.includes(MARKER));
  const escape = value => String(value).replace(/[<>&`\[\]\\]/g, '');
  const comment = ok ? `${MARKER}\nThank you! Every required author has signed CLA ${version}.` :
    `${MARKER}\nPlease read the [CLA](${claURL}) (version ${version}).\n\nWaiting for: ${unsigned.map(a => '@' + a).join(', ') || 'linked commit authors'}.\n\nSign by commenting exactly:\n\n\`\`\`\n${PHRASE}\n\`\`\`\n` +
    (unlinked.size ? `\nUnlinked commit email(s): ${[...unlinked].map(escape).join(', ')}. Link the email to your GitHub account or amend the commit; nobody can sign for an unlinked email.\n` : '') +
    '\nComment `recheck` after fixing commit authors.';
  if (existing) { if (existing.body !== comment) await github.rest.issues.updateComment({...repo, comment_id: existing.id, body: comment}); }
  else if (!ok) await github.rest.issues.createComment({...repo, issue_number: number, body: comment});
  return {ok, unsigned, unlinked: [...unlinked]};
}
module.exports = {run, PHRASE, MARKER};
