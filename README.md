# repo-template

新しいリポジトリの雛形。`.github/workflows/pr-review.yml` だけが入っている。

## 使い方

```bash
gh repo create tamura09/<NAME> --private --template tamura09/repo-template
```

作ったあとに [tamura09/github-terraform](https://github.com/tamura09/github-terraform)
の `locals.tf` へ追加すると、デフォルトブランチと `main` のブランチ保護、マージ方法が
Terraform の管理下に入る。追加のしかたはそちらの README にある。

## 入っているもの

### `.github/workflows/pr-review.yml`

`pr-review.yml` は [tamura09/claude-pr-review](https://github.com/tamura09/claude-pr-review)
の再利用可能ワークフローを呼ぶだけ。PR ごとに Claude がレビューを投稿し、
`claude-review` のチェックを出す。マージも承認もしない。

OAuth トークンはリポジトリの secret には置かない。AWS の SSM に1本だけ置いてあり、
呼び出されたワークフローが OIDC で読む。だから新しいリポジトリでも secret の登録は
要らない。

### `renovate.json`

依存の更新を [tamura09/renovate-runner](https://github.com/tamura09/renovate-runner)
に任せるための設定。共有プリセットを extends するだけで、リポジトリ固有の指定は
書かない。

**置いてあるだけでは動かない**。実際に更新 PR が来るのは
[tamura09/github-terraform](https://github.com/tamura09/github-terraform) の
`locals.tf` で `enable_renovate = true` を書いたリポジトリだけ。既定は無効なので、
テンプレートから作ったままでは Renovate は走らない。

有効にしたくなったら `locals.tf` にフラグを足す。このファイルは触らなくてよい。

```hcl
    <NAME> = {
      enable_renovate = true
    }
```

言語ごとの設定 (npm のグループ分けなど) が要るときは、このファイルに
`packageRules` を足すのではなく、まず共有プリセット側を直すか検討する。
1リポジトリにしか当てはまらない設定だけをここに書く。

## ここに置かないもの

言語ごとのCIやデプロイは、リポジトリによって中身が違いすぎるので入れていない。
必要になったら既存のリポジトリからコピーする。
