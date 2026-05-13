class Harborrs < Formula
  desc "Self-hosted single-binary RSS server (Google Reader API + htmx UI)"
  homepage "https://github.com/kfet/harborrs"
  url "https://github.com/kfet/harborrs.git", tag: "v0.1.0"
  license "MIT"
  head "https://github.com/kfet/harborrs.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X github.com/kfet/harborrs.Version=#{version}"
    system "go", "build", "-trimpath", "-ldflags=#{ldflags}",
           "-o", bin/"harborrs", "./cmd/harborrs"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/harborrs version")
    out = shell_output("#{bin}/harborrs init -data #{testpath}/data")
    assert_match "harborrs initialised", out
    assert_predicate testpath/"data/config.json", :exist?
  end
end
