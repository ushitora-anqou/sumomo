{
  description = "Sumomo — LibreTranslate-compatible API server backed by plamo-3-translate on llama.cpp";

  inputs = {
    nixpkgs.url = "https://channels.nixos.org/nixpkgs-unstable/nixexprs.tar.zst";
  };

  outputs = { self, nixpkgs }:
    let
      # Every output exists for both server architectures so that
      # `nix build .#sumomo-image` resolves to the current machine's
      # native system. CI uses this to build the arm64 image natively on
      # arm64 runners (no cross-compilation, no emulation).
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # Build the Go server for the given (native) package set.
      # "go install" names the binary after the module path's last
      # component, which is already "sumomo".
      sumomoFor = p: p.buildGoModule {
        pname = "sumomo";
        version = "0.1.0";
        src = ./server;
        vendorHash = null;
      };

      # OCI image wrapping the server and llama.cpp. The GGUF model is NOT
      # baked in; mount it at /models and pass --model /models/<file>.gguf.
      # imageName/imageTag are read by the release workflow via
      # `nix eval` to name the pushed image.
      sumomoImageFor = tag: p: server: p.dockerTools.buildImage {
        name = "ghcr.io/ushitora-anqou/sumomo";
        inherit tag;
        copyToRoot = p.buildEnv {
          name = "sumomo-image-root";
          paths = [
            server
            p.llama-cpp
          ];
          pathsToLink = [ "/bin" ];
        };
        # Writable /tmp for llama-server logs (non-root user).
        extraCommands = ''
          mkdir -p tmp
          chmod 1777 tmp
        '';
        config = {
          User = "65532";
          Entrypoint = [ "/bin/sumomo" ];
          Env = [ "LLAMA_SERVER_BIN=/bin/llama-server" ];
        };
      };

      pythonEnvFor = p: p.python313.withPackages (ps: with ps; [
        requests
      ]);
    in
    {
      devShells = forAllSystems (pkgs: {
        # Development shell, loaded via direnv (`use flake` in .envrc).
        # Contains everything the Make targets need, including the E2E
        # tooling (kind/kubectl/helm), so the Makefile can run commands
        # directly without wrapping them in `nix develop`.
        default = pkgs.mkShell {
          packages = [
            (pythonEnvFor pkgs)
            pkgs.go
            pkgs.golangci-lint
            pkgs.llama-cpp
            pkgs.kind
            pkgs.kubectl
            pkgs.kubernetes-helm
            pkgs.jq
            pkgs.curl
            pkgs.openssl
          ];
        };
      });

      packages = forAllSystems (pkgs:
        let
          sumomo = sumomoFor pkgs;
        in
        {
          inherit sumomo;
          sumomo-image = sumomoImageFor "latest" pkgs sumomo;
          default = sumomo;
        });

      apps = forAllSystems (pkgs:
        let
          sumomoDrv = sumomoFor pkgs;
        in
        rec {
          # Main: the LibreTranslate-compatible server.
          # nix run .#sumomo [-- --listen 127.0.0.1:5000]
          sumomo = {
            type = "app";
            meta.description = "LibreTranslate-compatible API server (sumomo)";
            program = "${pkgs.writeShellScript "run-sumomo" ''
              export LLAMA_SERVER_BIN="${pkgs.llama-cpp}/bin/llama-server"
              exec ${sumomoDrv}/bin/sumomo "$@"
            ''}";
          };

          # Sub: helper scripts.
          # nix run .#download-model [-- --quant Q4_K_M]
          download-model = {
            type = "app";
            meta.description = "Download the official plamo-3-translate-0.8b GGUF";
            program = "${pkgs.writeShellScript "sumomo-download-model" ''
              exec ${pythonEnvFor pkgs}/bin/python ${self}/scripts/download_model.py "$@"
            ''}";
          };

          # nix run .#translate -- --target-lang en "こんにちは"
          translate = {
            type = "app";
            meta.description = "Translate text from the command line";
            program = "${pkgs.writeShellScript "sumomo-translate" ''
              export LLAMA_SERVER_BIN="${pkgs.llama-cpp}/bin/llama-server"
              exec ${pythonEnvFor pkgs}/bin/python ${self}/scripts/translate.py "$@"
            ''}";
          };
        });
    };
}
