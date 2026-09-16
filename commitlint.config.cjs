// Commit message lint rules for Conventional Commits.
//
// Enforced by the commitlint job in the CI pipeline on every pull
// request; the rules mirror what semantic-release expects so that any
// commit passing this gate can be parsed into a version bump:
//   - feat    -> minor bump
//   - fix     -> patch bump
//   - BREAKING CHANGE footer or ! after the type -> major bump
//   - chore/docs/style/refactor/test/ci/build/perf/revert -> no bump
//
// Format: <type>(<scope>): <subject>
//   - blank line
//   - body (wrapped at 100 columns)
//   - blank line
//   - footer ("BREAKING CHANGE: ..." and/or "Refs: #123")
module.exports = {
  ignores: [
    // Allow the cloud-agent progress commit used to establish an initial plan.
    (message) => message.startsWith('Initial plan'),
  ],
  rules: {
    // ---- type ---- //
    'type-enum': [2, 'always', [
      'build', 'chore', 'ci', 'docs', 'feat', 'fix',
      'perf', 'refactor', 'revert', 'style', 'test',
    ]],
    'type-case':     [2, 'always', 'lower-case'],
    'type-empty':    [2, 'never'],

    // ---- scope ---- //
    'scope-case':    [2, 'always', 'lower-case'],
    'scope-empty':   [0], // optional

    // ---- subject ---- //
    'subject-case':        [2, 'never', ['sentence-case', 'start-case', 'pascal-case', 'upper-case']],
    'subject-empty':       [2, 'never'],
    'subject-full-stop':   [2, 'never', '.'],
    'subject-max-length':  [2, 'always', 100],

    // ---- header ---- //
    'header-max-length': [2, 'always', 100],

    // ---- body ---- //
    'body-leading-blank': [1, 'always'],
    'body-max-line-length': [2, 'always', 100],

    // ---- footer ---- //
    'footer-leading-blank': [1, 'always'],
    'footer-max-line-length': [2, 'always', 100],
  },
};
