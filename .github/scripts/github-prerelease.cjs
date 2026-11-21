"use strict";

async function ensureGitHubPrerelease({ github, core, repository, tag, targetSha, body }) {
  try {
    const { data } = await github.rest.repos.getReleaseByTag({ ...repository, tag });
    if (!data.prerelease) {
      core.setFailed(`${tag} exists as a non-prerelease GitHub Release`);
    } else {
      core.info(`${tag} prerelease already exists`);
    }
  } catch (error) {
    if (error.status !== 404) throw error;

    await github.rest.repos.createRelease({
      ...repository,
      tag_name: tag,
      target_commitish: targetSha,
      name: tag,
      body,
      prerelease: true,
    });
  }
}

module.exports = { ensureGitHubPrerelease };
