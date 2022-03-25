module.exports = {
  username: "renovate-release",
  gitAuthor: "Renovate Bot <bot@renovateapp.com>",
  platform: "github",
  includeForks: true,
  repositories: ["cterence/argo"],
  packageRules: [
    {
      matchUpdateTypes: ["pin", "digest", "patch", "minor"],
      ignoreTests: true,
      automerge: true,
    },
  ],
};
