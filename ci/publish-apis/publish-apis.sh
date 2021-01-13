git config user.name github-actions
git config user.email github-actions@github.com
git config --global url."git@github.com/solo-io/".insteadOf "https://github.com/solo-io/"
git checkout -b update-gloo-apis-github-action
git add .
git commit -m "Sync Gloo APIs to ${{ github.event.release.tag_name }}"
git push --set-upstream origin update-gloo-apis-github-action