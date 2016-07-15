FROM convox/rails

RUN apt-get update
RUN apt-get install -y imagemagick libmagickwand-dev curl tar

RUN curl -s https://bin.equinox.io/c/jWahGASjoRq/backplane-stable-linux-amd64.tgz | tar -xzC /
RUN /backplane update
RUN curl -s https://gist.githubusercontent.com/ddollar/46b07b39bfd15e191ff1a50300348a7b/raw/8bc22641ae2a8e421d63b864f7477c9d4c245873/backplane.sh -o /backplane.sh
RUN chmod +x /backplane.sh
ENTRYPOINT ["/backplane.sh"]

# copy only the files needed for bundle install
# uncomment the vendor/cache line if you `bundle package` your gems
COPY Gemfile      /app/Gemfile
COPY Gemfile.lock /app/Gemfile.lock
# COPY vendor/cache /app/vendor/cache
RUN bundle install

# copy just the files needed for assets:precompile
COPY Rakefile   /app/Rakefile
COPY config     /app/config
COPY public     /app/public
COPY app/assets /app/app/assets
RUN bundle exec rake assets:precompile

# copy the rest of the app
COPY . /app

CMD ["bundle", "exec", "puma", "-C", "config/puma.rb"]
