{
  description = "Sumomo — LibreTranslate-compatible API server backed by plamo-3-translate on llama.cpp";

  inputs = {
    nixpkgs.url = "https://channels.nixos.org/nixpkgs-unstable/nixexprs.tar.zst";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # Build the Go server for the given package set (native or cross).
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
      sumomoImageFor = tag: p: server: p.dockerTools.buildImage {
        name = "sumomo";
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

      sumomo = sumomoFor pkgs;
      sumomo-image = sumomoImageFor "latest" pkgs sumomo;

      # armv8 (aarch64) build, cross-compiled from x86_64 via nixpkgs
      # cross toolchains (no emulation required).
      pkgsArm = pkgs.pkgsCross.aarch64-multiplatform;
      sumomo-arm64 = sumomoFor pkgsArm;
      sumomo-image-arm64 = sumomoImageFor "latest-arm64" pkgsArm sumomo-arm64;

      pythonEnv = pkgs.python313.withPackages (ps: with ps; [
        requests
      ]);
    in
    {
      devShells.${system} = {
        # Development: Go + Python + llama.cpp CLI tools.
        default = pkgs.mkShell {
          packages = [
            pythonEnv
            pkgs.go
            pkgs.llama-cpp
          ];
        };

        # Everything scripts/e2e.sh needs (kind cluster + Mastodon).
        e2e = pkgs.mkShell {
          packages = with pkgs; [
            kind
            kubectl
            kubernetes-helm
            jq
            curl
            openssl
            python3
          ];
        };
      };

      packages.${system} = {
        inherit
          sumomo
          sumomo-image
          sumomo-arm64
          sumomo-image-arm64;
        default = sumomo;
      };

      apps.${system} = {
        # Main: the LibreTranslate-compatible server.
        # nix run .#sumomo [-- --listen 127.0.0.1:5000]
        sumomo = {
          type = "app";
          meta.description = "LibreTranslate-compatible API server (sumomo)";
          program = "${pkgs.writeShellScript "run-sumomo" ''
            export LLAMA_SERVER_BIN="${pkgs.llama-cpp}/bin/llama-server"
            exec ${sumomo}/bin/sumomo "$@"
          ''}";
        };

        # Sub: helper scripts.
        # nix run .#download-model [-- --quant Q4_K_M]
        download-model = {
          type = "app";
          meta.description = "Download the official plamo-3-translate-0.8b GGUF";
          program = "${pkgs.writeShellScript "sumomo-download-model" ''
            exec ${pythonEnv}/bin/python ${self}/scripts/download_model.py "$@"
          ''}";
        };

        # nix run .#translate -- --target-lang en "こんにちは"
        translate = {
          type = "app";
          meta.description = "Translate text from the command line";
          program = "${pkgs.writeShellScript "sumomo-translate" ''
            export LLAMA_SERVER_BIN="${pkgs.llama-cpp}/bin/llama-server"
            exec ${pythonEnv}/bin/python ${self}/scripts/translate.py "$@"
          ''}";
        };

        default = self.apps.${system}.sumomo;
      };
    };
}
