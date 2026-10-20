'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {run, PHRASE, MARKER} = require('../.github/scripts/cla.cjs');
function mock() {
  const state = {entries: [], comments: [], statuses: [], writes: 0, conflicts: 0, branch: false,
    commits: [{author: {id: 7, login: 'alice', type: 'User'}, commit: {author: {email: 'alice@example.com'}}}],
    pr: {number: 1, state: 'open', head: {sha: 'head'}}};
  const notFound = () => {throw Object.assign(new Error('missing'), {status: 404});};
  const github = {rest: {pulls: {
    get: async () => ({data: state.pr}), listCommits: async () => ({data: state.commits})},
    repos: {
      getContent: async () => state.writes ? {data: {sha: String(state.writes), content: Buffer.from(JSON.stringify(state.entries)).toString('base64')}} : notFound(),
      createOrUpdateFileContents: async p => {if (state.conflicts-- > 0) throw Object.assign(new Error('conflict'), {status: 409}); state.entries = JSON.parse(Buffer.from(p.content, 'base64')); state.writes++;},
      createCommitStatus: async p => state.statuses.push(p)},
    git: {getRef: async p => p.ref === 'heads/cla-signatures' && !state.branch ? notFound() : {data: {object: {sha: 'base'}}}, createRef: async () => {state.branch = true;}},
    issues: {listComments: async () => ({data: state.comments}),
      createComment: async p => state.comments.push({id: 3, body: p.body, user: {type: 'Bot'}}),
      updateComment: async p => {state.comments.find(c => c.id === p.comment_id).body = p.body;}}},
    paginate: async (fn, p) => (await fn(p)).data};
  const context = {repo: {owner: 'owner', repo: 'cronpad'}, eventName: 'pull_request_target', payload: {
    repository: {default_branch: 'main'}, pull_request: {number: 1}}};
  const comment = body => {context.eventName = 'issue_comment';context.payload.issue = {number: 1, pull_request: {}};context.payload.comment = {body, user: {id: 7, login: 'alice'}, created_at: '2026-10-16T00:00:00Z', html_url: 'https://github.com/o/r/issues/1#issuecomment-1'};};
  return {github, context, state, comment};
}
test('unsigned, recheck, sign, duplicate sign; one bot comment edited', async () => {
  const m = mock();assert.equal((await run(m)).ok, false);assert.equal(m.state.statuses.at(-1).state, 'failure');assert.equal(m.state.comments.length, 1);
  m.comment('recheck');await run(m);assert.equal(m.state.comments.length, 1);
  m.comment('  ' + PHRASE + '\n');assert.equal((await run(m)).ok, true);assert.equal(m.state.statuses.at(-1).state, 'success');assert.match(m.state.comments[0].body, /Thank you/);
  assert.deepEqual(m.state.entries[0], {id: 7, login: 'alice', cla_version: '1.2', signed_at: m.context.payload.comment.created_at, pull_request: 1, comment_url: m.context.payload.comment.html_url});
  await run(m);assert.equal(m.state.entries.length, 1);assert.equal(m.state.writes, 1);
});
test('bots and case-insensitive allowlisted maintainers are exempt; no all-signed comment', async () => {
  const m = mock();m.maintainers = 'ALICE';m.state.commits.push({author: {id: 8, login: 'ci', type: 'Bot'}}, {author: {id: 9, login: 'test[bot]', type: 'User'}});
  assert.equal((await run(m)).ok, true);assert.equal(m.state.comments.length, 0);
});
test('unlinked email blocks with remediation', async () => {
  const m = mock();m.state.commits[0].author = null;assert.equal((await run(m)).ok, false);assert.match(m.state.comments[0].body, /amend the commit/);
});
test('conflict retries by rereading signatures', async () => {
  const m = mock();m.state.conflicts = 2;m.comment(PHRASE);assert.equal((await run(m)).ok, true);assert.equal(m.state.writes, 1);
});
test('three conflicts fail explicitly', async () => {const m = mock();m.state.conflicts = 3;m.comment(PHRASE);await assert.rejects(run(m), /conflict/);});
test('signature only counts numeric ID and current version', async () => {
  const m = mock();m.state.writes = 1;m.state.entries = [{id: 6, login: 'alice', cla_version: '1.2'}, {id: 7, login: 'alice', cla_version: '0.9'}];assert.equal((await run(m)).ok, false);
  m.state.entries.push({id: 7, login: 'old-login', cla_version: '1.2'});assert.equal((await run(m)).ok, true);
});
test('closed PR, issue comment and unrelated comment do nothing; substring does not sign', async () => {
  const m = mock();m.state.pr.state = 'closed';assert.equal(await run(m), undefined);assert.equal(m.state.statuses.length, 0);
  m.state.pr.state = 'open';m.comment('unrelated');assert.equal(await run(m), undefined);
  m.comment(PHRASE);delete m.context.payload.issue.pull_request;assert.equal(await run(m), undefined);
  m.comment('I quote: ' + PHRASE);await run(m);assert.equal(m.state.writes, 0);
});
test('human marker cannot impersonate bot comment; statuses truncate to 140 chars', async () => {
  const m = mock();m.state.comments.push({id: 90, body: MARKER, user: {type: 'User'}});m.state.commits = Array.from({length: 20}, (_, i) => ({author: {id: i, login: 'long_login_' + i, type: 'User'}}));await run(m);assert.equal(m.state.comments.length, 2);assert.ok(m.state.statuses[0].description.length <= 140);
});

test('CLA document, contributor guide and workflow use the default signature version', async () => {
  const document = readFileSync(require.resolve('../CLA.md'), 'utf8');
  const workflow = readFileSync(require.resolve('../.github/workflows/cla.yml'), 'utf8');
  const guide = readFileSync(require.resolve('../CONTRIBUTING.md'), 'utf8');
  const version = document.match(/^# Individual Contributor License Agreement — version (\d+\.\d+)/)[1];
  assert.ok(workflow.includes(`CLA_VERSION: '${version}'`));
  assert.ok(guide.includes(`[CLA version ${version}]`));
  const m = mock();m.comment(PHRASE);await run(m);
  assert.equal(m.state.entries[0].cla_version, version);
});

for (const previous of ['1.0', '1.1']) {
  test(`a ${previous}-only signer must re-sign 1.2; signing preserves the old grant record`, async () => {
    const m = mock();m.version = '1.2';m.state.writes = 1;
    m.state.entries = [{id: 7, login: 'alice', cla_version: previous}];
    assert.equal((await run(m)).ok, false);
    assert.equal(m.state.statuses.at(-1).state, 'failure');
    assert.match(m.state.comments[0].body, /version 1\.2/);
    assert.ok(m.state.comments[0].body.includes(PHRASE));
    m.comment(PHRASE);assert.equal((await run(m)).ok, true);
    assert.equal(m.state.statuses.at(-1).state, 'success');
    assert.deepEqual(m.state.entries.map(e => e.cla_version), [previous, '1.2']);
    await run(m);assert.equal(m.state.entries.length, 2);
  });
}
test('a 1.2 signature passes without re-signing', async () => {
  const m = mock();m.version = '1.2';m.state.writes = 1;
  m.state.entries = [{id: 7, login: 'alice', cla_version: '1.2'}];
  assert.equal((await run(m)).ok, true);
  assert.equal(m.state.statuses.at(-1).state, 'success');
  assert.equal(m.state.comments.length, 0);
});
