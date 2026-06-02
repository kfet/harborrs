class Harb < Formula
  desc "Self-hosted single-binary RSS server (Google Reader API + htmx UI)"
  homepage "https://github.com/kfet/harb"
  url "https://github.com/kfet/harb.git", tag: "v0.1.0"
  license "MIT"
  head "https://github.com/kfet/harb.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X github.com/kfet/harb.Version=#{version}"
    system "go", "build", "-trimpath", "-ldflags=#{ldflags}",
           "-o", bin/"harb", "./cmd/harb"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/harb version")
    out = shell_output("#{bin}/harb init -data #{testpath}/data")
    assert_match "harb initialised", out
    assert_predicate testpath/"data/config.json", :exist?
  end
end
