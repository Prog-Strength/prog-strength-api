// semantic-release configuration. Triggered by .github/workflows/release.yml
// on every push to main. Analyzes conventional commits since the last tag,
// determines the next semver, writes the changelog, commits + tags, and
// publishes a GitHub Release. The deploy job in release.yml runs only when
// a new version is actually published (see job output gating).

module.exports = {
  branches: ['main'],
  plugins: [
    '@semantic-release/commit-analyzer',
    '@semantic-release/release-notes-generator',
    [
      '@semantic-release/changelog',
      {
        changelogFile: 'CHANGELOG.md',
      },
    ],
    [
      '@semantic-release/git',
      {
        assets: ['CHANGELOG.md', 'package.json', 'package-lock.json'],
        // [skip ci] keeps this changelog commit — pushed to main by the
        // release bot via RELEASE_BOT_TOKEN — from triggering a second
        // release run (it's a chore commit, so that run would be a no-op).
        message: 'chore(release): ${nextRelease.version} [skip ci]\n\n${nextRelease.notes}',
      },
    ],
    '@semantic-release/github',
  ],
};
